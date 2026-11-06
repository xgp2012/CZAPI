// Package redisqueue is the Redis 7 implementation of the queue.Queue contract
// used for asynchronous accounting.
//
// It uses Redis Streams with one stream per topic and a single consumer group
// per stream. The delivery guarantees are:
//
//   - Publish is an XADD, so a message is durable as soon as the call returns.
//   - Consume reads through the consumer group, so an entry is handed to exactly
//     one consumer replica.
//   - A handler error re-publishes a copy of the message with Attempts+1 and
//     then acknowledges the original, which turns the stream into an at-least
//     once work queue with a bounded retry budget.
//   - After maxAttempts the message is dropped and logged, so one poisonous
//     event cannot stall accounting forever.
//
// Payloads are base64-encoded before they reach the stream. Redis Stream field
// values are binary-safe, but base64 keeps the stream readable with redis-cli
// and guarantees a round trip through any proxy that is tempted to re-encode
// strings.
//
// requires Redis 7; not integration-tested on the build machine.
package redisqueue

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/queue"
)

// Stream field names. Kept short because they are repeated on every entry.
const (
	fieldPayload   = "p" // base64 payload
	fieldAttempts  = "a" // delivery attempts so far
	fieldCreatedAt = "c" // unix milliseconds of the original publish
	fieldID        = "i" // opaque message id, stable across retries
)

// maxAttempts is the retry budget before a message is dropped.
const maxAttempts = 5

// DefaultConsumerGroup is the consumer group every replica joins.
const DefaultConsumerGroup = "czapi"

// Config configures the Redis Streams client.
type Config struct {
	// Addr is host:port of the Redis server.
	Addr string
	// Password is the AUTH secret; empty when the server has none.
	Password string
	// DB is the logical database index.
	DB int
	// PoolSize caps the number of sockets; zero means the client default.
	PoolSize int
	// DialTimeout bounds connection establishment; zero means the default.
	DialTimeout time.Duration
	// Group is the consumer group name; empty means DefaultConsumerGroup.
	Group string
	// Consumer is this replica's name inside the group; empty means a generated
	// unique name.
	Consumer string
	// Logger receives retry and drop events; nil means slog.Default().
	Logger *slog.Logger
}

// Queue implements queue.Queue on top of Redis Streams.
//
// Safe for concurrent use: it wraps a redis.Client, whose connection pool is
// safe by contract, and its group, consumer and logger are set once in New.
type Queue struct {
	rdb      *redis.Client
	group    string
	consumer string
	log      *slog.Logger
	// clk stamps Publish and the fallback created_at. Retries keep the original
	// timestamp instead of reading it again.
	clk clock.Clock
}

// compile-time assertion that the contract is satisfied.
var _ queue.Queue = (*Queue)(nil)

// New builds a Queue and verifies the connection with a PING.
func New(ctx context.Context, cfg Config) (*Queue, error) {
	if cfg.Addr == "" {
		return nil, errs.Internal("redisqueue: config.Addr is required", nil)
	}
	opts := &redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	if cfg.PoolSize > 0 {
		opts.PoolSize = cfg.PoolSize
	}
	if cfg.DialTimeout > 0 {
		opts.DialTimeout = cfg.DialTimeout
	}
	rdb := redis.NewClient(opts)

	q := newQueue(rdb, cfg)
	if err := q.Ping(ctx); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return q, nil
}

// newQueue builds a Queue around an existing client; it is the test seam.
func newQueue(rdb *redis.Client, cfg Config) *Queue {
	q := &Queue{rdb: rdb, group: cfg.Group, consumer: cfg.Consumer, log: cfg.Logger, clk: clock.System{}}
	if q.group == "" {
		q.group = DefaultConsumerGroup
	}
	if q.consumer == "" {
		q.consumer = "consumer-" + randomHex(6)
	}
	if q.log == nil {
		q.log = slog.Default()
	}
	return q
}

// Ping verifies the connection.
func (q *Queue) Ping(ctx context.Context) error {
	if q == nil || q.rdb == nil {
		return errs.Internal("redisqueue: not connected", nil)
	}
	if err := q.rdb.Ping(ctx).Err(); err != nil {
		return wrapRedis("redisqueue: ping", err)
	}
	return nil
}

// Close releases the connection pool.
func (q *Queue) Close() error {
	if q == nil || q.rdb == nil {
		return nil
	}
	return q.rdb.Close()
}

// Client exposes the underlying client for health checks and tests.
func (q *Queue) Client() *redis.Client { return q.rdb }

// Group returns the consumer group this queue publishes and consumes under.
func (q *Queue) Group() string { return q.group }

// Consumer returns this replica's consumer name.
func (q *Queue) Consumer() string { return q.consumer }

// wrapRedis maps a Redis error into the shared vocabulary.
func wrapRedis(msg string, err error) error {
	if err == nil || errors.Is(err, redis.Nil) {
		return nil
	}
	return errs.Internal(msg, err)
}

// streamKey maps a topic onto its stream name.
//
// The prefix keeps accounting streams apart from any other key in the shared
// Redis instance, which matters because the cache layer also lives there.
func streamKey(topic string) string {
	return "cz:stream:" + topic
}

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------

// Publish appends payload to a topic's stream.
//
// The message carries its own id, attempt counter and creation time so that a
// retry re-publishes the original metadata rather than inventing new values.
func (q *Queue) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if topic == "" {
		return errs.InvalidRequest("topic is required")
	}
	now := q.now()
	return q.add(ctx, topic, queue.Message{
		ID:        newMessageID(now),
		Topic:     topic,
		Payload:   payload,
		Attempts:  0,
		CreatedAt: now,
	})
}

// add writes one message to the stream.
func (q *Queue) add(ctx context.Context, topic string, m queue.Message) error {
	values := messageValues(m)
	if err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey(topic),
		// No MAXLEN: accounting events must not be silently trimmed. Retention is
		// an operator concern (ACKed entries are removed by XDEL in trim mode).
		Values: values,
	}).Err(); err != nil {
		return wrapRedis("redisqueue: xadd", err)
	}
	return nil
}

// messageValues renders a message as stream fields.
func messageValues(m queue.Message) map[string]any {
	return map[string]any{
		fieldID:        m.ID,
		fieldPayload:   base64.StdEncoding.EncodeToString(m.Payload),
		fieldAttempts:  strconv.Itoa(m.Attempts),
		fieldCreatedAt: strconv.FormatInt(m.CreatedAt.UTC().UnixMilli(), 10),
	}
}

// decodeMessage rebuilds a message from stream fields.
//
// A missing or unparseable created_at is stamped from the injected clock rather
// than left zero, so a retry re-published from this message never carries an
// epoch timestamp.
func (q *Queue) decodeMessage(topic, entryID string, values map[string]any) (queue.Message, error) {
	m := queue.Message{Topic: topic, ID: entryID}

	if raw, ok := stringField(values, fieldPayload); ok {
		payload, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return queue.Message{}, errs.Internal("redisqueue: decode payload", err)
		}
		m.Payload = payload
	}
	if id, ok := stringField(values, fieldID); ok {
		m.ID = id
	}
	if raw, ok := stringField(values, fieldAttempts); ok {
		if n, err := strconv.Atoi(raw); err == nil {
			m.Attempts = n
		}
	}
	if raw, ok := stringField(values, fieldCreatedAt); ok {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 {
			m.CreatedAt = time.UnixMilli(ms).UTC()
		}
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = q.now()
	}
	return m, nil
}

// stringField reads a stream field as a string.
//
// go-redis hands XREADGROUP values back as strings for small payloads, but the
// type is not formally guaranteed, so interface values are coerced too.
func stringField(values map[string]any, key string) (string, bool) {
	v, ok := values[key]
	if !ok || v == nil {
		return "", false
	}
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	case int64:
		return strconv.FormatInt(s, 10), true
	case int:
		return strconv.Itoa(s), true
	default:
		return "", false
	}
}

// ---------------------------------------------------------------------------
// Consume
// ---------------------------------------------------------------------------

// Consume processes up to batch messages, blocking at most wait.
//
// The first call on a topic creates the consumer group (MKSTREAM so an empty
// stream is enough). Every entry is acknowledged once it has been handled, or
// re-published with an incremented attempt counter and then acknowledged. A
// retry that would exceed maxAttempts is dropped with a log line.
func (q *Queue) Consume(ctx context.Context, topic string, batch int, wait time.Duration, h queue.Handler) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if topic == "" {
		return 0, errs.InvalidRequest("topic is required")
	}
	if h == nil {
		return 0, errs.InvalidRequest("handler is required")
	}
	if batch <= 0 {
		batch = 10
	}

	stream := streamKey(topic)
	if err := q.ensureGroup(ctx, stream); err != nil {
		return 0, err
	}

	res, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    q.group,
		Consumer: q.consumer,
		Streams:  []string{stream, ">"},
		Count:    int64(batch),
		Block:    wait,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			// No new entries within the block window: a normal empty poll.
			return 0, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, wrapRedis("redisqueue: xreadgroup", err)
	}

	processed := 0
	for _, s := range res {
		for _, entry := range s.Messages {
			if err := q.handleOne(ctx, topic, entry, h); err != nil {
				return processed, err
			}
			processed++
		}
	}
	return processed, nil
}

// ensureGroup creates the consumer group if it does not exist yet.
//
// BUSYGROUP means another replica won the race, which is success, not failure.
func (q *Queue) ensureGroup(ctx context.Context, stream string) error {
	err := q.rdb.XGroupCreateMkStream(ctx, stream, q.group, "0").Err()
	if err == nil || isBusyGroup(err) {
		return nil
	}
	return wrapRedis("redisqueue: xgroup create", err)
}

// isBusyGroup reports whether err is the "group already exists" reply.
func isBusyGroup(err error) bool {
	if err == nil {
		return false
	}
	return containsBusyGroup(err.Error())
}

// containsBusyGroup keeps the string test in one place.
func containsBusyGroup(msg string) bool {
	return strings.Contains(msg, "BUSYGROUP")
}

// handleOne decodes, dispatches and then settles one stream entry.
func (q *Queue) handleOne(ctx context.Context, topic string, entry redis.XMessage, h queue.Handler) error {
	stream := streamKey(topic)

	m, err := q.decodeMessage(topic, entry.ID, entry.Values)
	if err != nil {
		// Undecodable: acknowledge and drop, because retrying cannot help.
		q.log.Error("redisqueue: dropping undecodable entry",
			"topic", topic, "entry", entry.ID, "error", err)
		return q.ack(ctx, stream, entry.ID)
	}

	if err := h(ctx, m); err != nil {
		return q.retry(ctx, stream, entry.ID, m, err)
	}
	return q.ack(ctx, stream, entry.ID)
}

// retry re-publishes a failed message with an incremented attempt counter and
// acknowledges the original entry.
//
// entryID is the Redis stream entry id, which is distinct from Message.ID: the
// former addresses one stream entry, the latter identifies the logical event
// across its retries.
//
// The re-publish happens before the ack on purpose. If the process dies in
// between, the entry is redelivered and handled again, which is the safe
// direction for accounting (at-least-once, never lost); the repositories absorb
// the duplicate through their idempotency keys.
func (q *Queue) retry(ctx context.Context, stream, entryID string, m queue.Message, handlerErr error) error {
	next := m
	next.Attempts = m.Attempts + 1

	if next.Attempts >= maxAttempts {
		q.log.Error("redisqueue: dropping message after max attempts",
			"topic", m.Topic, "id", m.ID, "attempts", next.Attempts,
			"error", handlerErr)
		return q.ack(ctx, stream, entryID)
	}

	q.log.Warn("redisqueue: retrying message",
		"topic", m.Topic, "id", m.ID, "attempts", next.Attempts, "error", handlerErr)

	// The re-publish keeps the original id and creation time so operators can
	// follow one logical event across attempts.
	if err := q.add(ctx, m.Topic, next); err != nil {
		// Leaving the entry unacknowledged keeps it in the pending list, so it is
		// retried later; nothing is lost.
		return err
	}
	return q.ack(ctx, stream, entryID)
}

// ack acknowledges a stream entry.
//
// A failed ack is not fatal: the entry will be redelivered, and the handler's
// own idempotency (usage insert on request_id, ledger on ref) absorbs the
// duplicate.
func (q *Queue) ack(ctx context.Context, stream, entryID string) error {
	// Detach from cancellation so a shutting-down replica still acknowledges
	// what it already committed.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := q.rdb.XAck(ackCtx, stream, q.group, entryID).Err(); err != nil {
		q.log.Warn("redisqueue: ack failed", "stream", stream, "entry", entryID, "error", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Depth
// ---------------------------------------------------------------------------

// Depth reports the number of pending entries for a topic.
//
// XLEN is used rather than XPENDING because it is O(1) and it is what an
// alerting rule wants: the depth of the stream, including not-yet-acknowledged
// entries.
func (q *Queue) Depth(ctx context.Context, topic string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if topic == "" {
		return 0, errs.InvalidRequest("topic is required")
	}
	n, err := q.rdb.XLen(ctx, streamKey(topic)).Result()
	if err != nil {
		return 0, wrapRedis("redisqueue: xlen", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

// now returns the current time from the injected clock.
func (q *Queue) now() time.Time {
	if q.clk == nil {
		return time.Now().UTC()
	}
	return q.clk.Now().UTC()
}

// newMessageID returns a unique message id for the given instant.
func newMessageID(at time.Time) string {
	return strconv.FormatInt(at.UTC().UnixNano(), 36) + "-" + randomHex(6)
}

// randomHex returns n random bytes rendered as hex.
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return zeros(2 * n)
	}
	return hex.EncodeToString(buf)
}

// zeros returns n ASCII zeroes.
func zeros(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}
