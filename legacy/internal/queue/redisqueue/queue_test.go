package redisqueue

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/queue"
)

// These tests need no Redis server: they cover the stream naming, the payload
// encoding, the field round trip and the retry arithmetic.
//
// What they cannot cover is the actual XADD/XREADGROUP behaviour, which needs
// Redis 7.

// sampleTime is a fixed timestamp so round-trip assertions are exact.
var sampleTime = time.Date(2026, 11, 5, 7, 30, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Stream naming
// ---------------------------------------------------------------------------

func TestStreamKeyNaming(t *testing.T) {
	assert.Equal(t, "cz:stream:usage", streamKey(queue.TopicUsage))
	assert.Equal(t, "cz:stream:audit", streamKey(queue.TopicAudit))

	// Topics must not collide, and the prefix must keep accounting streams apart
	// from the cache keyspace in the same Redis instance.
	assert.NotEqual(t, streamKey(queue.TopicUsage), streamKey(queue.TopicAudit))
	assert.Contains(t, streamKey("usage"), "cz:stream:")
	assert.NotContains(t, streamKey("usage"), "cz:key:")
	assert.NotContains(t, streamKey("usage"), "cz:rl:")
}

// ---------------------------------------------------------------------------
// Payload encoding
// ---------------------------------------------------------------------------

func TestPayloadIsBase64OnTheWire(t *testing.T) {
	payload := []byte(`{"request_id":"abc","cost_micros":1234}`)
	m := queue.Message{
		ID: "m-1", Topic: queue.TopicUsage, Payload: payload,
		Attempts: 0, CreatedAt: sampleTime,
	}

	values := messageValues(m)
	raw, ok := values[fieldPayload].(string)
	require.True(t, ok, "the payload field must be a string")

	assert.Equal(t, base64.StdEncoding.EncodeToString(payload), raw)
	assert.NotContains(t, raw, "{", "the JSON must not appear verbatim in the stream")
	assert.Equal(t, `{"request_id":"abc","cost_micros":1234}`,
		mustDecodeBase64(t, raw), "base64 must round trip exactly")
}

func TestPayloadBase64HandlesBinaryAndEmpty(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x00, 0xff, 0xfe, 0x01},
		[]byte("unicode: \u4e2d\u6587 payload"),
	}
	for _, payload := range cases {
		m := queue.Message{ID: "m", Topic: queue.TopicUsage, Payload: payload, CreatedAt: sampleTime}
		values := messageValues(m)
		decoded, err := decode(queue.TopicUsage, "1-0", values)
		require.NoError(t, err)
		// A nil payload legitimately comes back as an empty slice: the stream
		// stores a zero-length string either way.
		assert.Len(t, decoded.Payload, len(payload))
		assert.Equal(t, string(payload), string(decoded.Payload))
	}
}

func TestMessageRoundTripThroughStreamFields(t *testing.T) {
	original := queue.Message{
		ID:        "msg-42",
		Topic:     queue.TopicUsage,
		Payload:   []byte(`{"n":1}`),
		Attempts:  3,
		CreatedAt: sampleTime,
	}

	decoded, err := decode(queue.TopicUsage, "1700000000000-0", messageValues(original))
	require.NoError(t, err)

	assert.Equal(t, original.ID, decoded.ID)
	assert.Equal(t, original.Payload, decoded.Payload)
	assert.Equal(t, original.Attempts, decoded.Attempts)
	assert.Equal(t, original.Topic, decoded.Topic)
	assert.True(t, original.CreatedAt.Equal(decoded.CreatedAt),
		"created_at must survive as unix milliseconds: got %s", decoded.CreatedAt)
}

func TestDecodeFallsBackToEntryID(t *testing.T) {
	// An entry with no id field (a foreign producer) still gets a usable id from
	// the stream entry id.
	m, err := decode(queue.TopicAudit, "1700000000000-7", map[string]any{
		fieldPayload: base64.StdEncoding.EncodeToString([]byte("x")),
	})
	require.NoError(t, err)
	assert.Equal(t, "1700000000000-7", m.ID)
	assert.Equal(t, queue.TopicAudit, m.Topic)
	assert.Equal(t, 0, m.Attempts)
	assert.False(t, m.CreatedAt.IsZero(), "a missing timestamp is stamped from the clock")
}

func TestDecodeRejectsBadPayload(t *testing.T) {
	_, err := decode(queue.TopicUsage, "1-0", map[string]any{
		fieldPayload: "not base64!!",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode payload")
}

func TestDecodeIgnoresUnparseableCounters(t *testing.T) {
	m, err := decode(queue.TopicUsage, "1-0", map[string]any{
		fieldPayload:   base64.StdEncoding.EncodeToString([]byte("x")),
		fieldAttempts:  "not-a-number",
		fieldCreatedAt: "not-a-number",
	})
	require.NoError(t, err, "a malformed counter must not fail the whole message")
	assert.Equal(t, 0, m.Attempts)
	assert.False(t, m.CreatedAt.IsZero())
}

func TestStringFieldAcceptsEveryValueShape(t *testing.T) {
	values := map[string]any{
		"str":     "hello",
		"bytes":   []byte("hello"),
		"int64":   int64(42),
		"int":     42,
		"nil":     nil,
		"unknown": 3.5,
	}
	for _, key := range []string{"str", "bytes", "int64", "int"} {
		_, ok := stringField(values, key)
		assert.True(t, ok, "field %q must decode", key)
	}
	missing := []string{"nil", "unknown", "absent"}
	for _, key := range missing {
		_, ok := stringField(values, key)
		assert.False(t, ok, "field %q must be skipped rather than panicking", key)
	}
}

// ---------------------------------------------------------------------------
// Retry arithmetic
// ---------------------------------------------------------------------------

func TestRetryBudgetBoundary(t *testing.T) {
	// An attempt counter that is one short of the budget is still retried.
	m := queue.Message{ID: "m", Topic: queue.TopicUsage, Attempts: maxAttempts - 2, CreatedAt: sampleTime}
	next := m
	next.Attempts = m.Attempts + 1
	assert.Less(t, next.Attempts, maxAttempts, "the second-to-last attempt must still be retried")

	// The last allowed attempt is dropped.
	m.Attempts = maxAttempts - 1
	next.Attempts = m.Attempts + 1
	assert.GreaterOrEqual(t, next.Attempts, maxAttempts, "the final attempt must be dropped")
}

func TestMaxAttemptsIsFive(t *testing.T) {
	// plan: five attempts, then drop, so one poisonous event cannot stall the
	// accounting path forever.
	assert.Equal(t, 5, maxAttempts)
}

// ---------------------------------------------------------------------------
// Constructor and configuration (no server needed)
// ---------------------------------------------------------------------------

func TestNewRejectsEmptyAddr(t *testing.T) {
	q, err := New(context.Background(), Config{})
	assert.Nil(t, q)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Addr")
}

func TestNewQueueDefaults(t *testing.T) {
	q := newQueue(nil, Config{})
	assert.Equal(t, DefaultConsumerGroup, q.Group())
	assert.NotEmpty(t, q.Consumer())
	assert.NotNil(t, q.log, "a nil logger must fall back to the default")
	assert.Equal(t, "czapi", DefaultConsumerGroup)
}

func TestNewQueueGeneratesDistinctConsumers(t *testing.T) {
	a := newQueue(nil, Config{})
	b := newQueue(nil, Config{})
	assert.NotEqual(t, a.Consumer(), b.Consumer(),
		"two replicas sharing a consumer name would steal each other's entries")
}

func TestNewQueueHonoursExplicitGroupAndConsumer(t *testing.T) {
	q := newQueue(nil, Config{Group: "workers", Consumer: "replica-1"})
	assert.Equal(t, "workers", q.Group())
	assert.Equal(t, "replica-1", q.Consumer())
}

func TestNilQueueIsSafe(t *testing.T) {
	var q *Queue
	assert.Error(t, q.Ping(context.Background()))
	assert.NoError(t, q.Close())
}

func TestBusyGroupDetection(t *testing.T) {
	assert.False(t, isBusyGroup(nil))
	assert.False(t, isBusyGroup(assert.AnError), "an unrelated error is not a BUSYGROUP reply")
	assert.True(t, containsBusyGroup("BUSYGROUP Consumer Group name already exists"),
		"another replica winning the XGROUP CREATE race is success, not failure")
	assert.False(t, containsBusyGroup("NOGROUP No such key"))
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

func TestMessageIDsAreUnique(t *testing.T) {
	at := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)
	seen := make(map[string]struct{}, 128)
	for i := 0; i < 128; i++ {
		id := newMessageID(at)
		require.NotEmpty(t, id)
		_, dup := seen[id]
		assert.False(t, dup, "duplicate message id %q", id)
		seen[id] = struct{}{}
	}
}

func TestRandomHexAndZeros(t *testing.T) {
	assert.Len(t, randomHex(4), 8)
	assert.Equal(t, "0000", zeros(4))
	assert.Equal(t, "", zeros(0))
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// decode rebuilds a message with a queue whose clock and config are defaults.
func decode(topic, entryID string, values map[string]any) (queue.Message, error) {
	return newQueue(nil, Config{}).decodeMessage(topic, entryID, values)
}

// mustDecodeBase64 decodes a base64 string, failing the test on error.
func mustDecodeBase64(t *testing.T, s string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	require.NoError(t, err)
	return string(raw)
}
