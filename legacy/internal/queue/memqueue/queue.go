// Package memqueue is the in-process implementation of the queue.Queue
// contract.
//
// It exists so accounting events flow end-to-end without Redis: every topic has
// one buffered channel, Publish appends to it, and Consume drains a batch. A
// handler error re-enqueues the message with Attempts+1; a message that fails
// MaxAttempts times is dropped and reported on stderr so a poisoned payload can
// never wedge a topic.
//
// Delivery is at-least-once within the process: a failed message is pushed back
// to the tail of its topic, so a permanently failing head does not block the
// rest. Delivery provides no durability across restarts 鈥?that is what the
// redis driver is for.
//
// Concurrency: Safe for concurrent use.
package memqueue

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/idgen"
	"czapi/internal/queue"
)

// Compile-time proof that the memory driver satisfies the frozen contract.
var _ queue.Queue = (*Queue)(nil)

const (
	// TopicBuffer is the per-topic channel capacity.
	TopicBuffer = 10000
	// MaxAttempts is how often a message is retried before it is dropped.
	MaxAttempts = 5
	// DefaultBatch is used when Consume is called with batch <= 0.
	DefaultBatch = 100
)

// topic is one buffered channel plus its own mutex for depth accounting.
type topic struct {
	mu    sync.Mutex
	ch    chan queue.Message
	depth int64
}

// Queue is an in-memory queue.Queue implementation.
//
// Safe for concurrent use.
type Queue struct {
	buffer int
	stderr io.Writer
	clk    clock.Clock

	mu     sync.RWMutex
	topics map[string]*topic

	closeOnce sync.Once
	closed    bool
}

// New returns an empty queue with the default per-topic buffer and the system
// clock.
func New() *Queue { return NewWithBuffer(TopicBuffer) }

// NewWithClock returns an empty queue that stamps messages with c. A nil clock
// falls back to clock.System.
func NewWithClock(c clock.Clock) *Queue {
	q := New()
	if c != nil {
		q.clk = c
	}
	return q
}

// NewWithBuffer returns an empty queue whose topics buffer capacity messages.
// A non-positive capacity falls back to TopicBuffer.
func NewWithBuffer(capacity int) *Queue {
	if capacity <= 0 {
		capacity = TopicBuffer
	}
	return &Queue{
		buffer: capacity,
		stderr: os.Stderr,
		clk:    clock.System{},
		topics: make(map[string]*topic),
	}
}

// SetStderr redirects where dropped-message notices are written. Passing nil
// restores os.Stderr. It is meant for tests and diagnostics.
func (q *Queue) SetStderr(w io.Writer) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if w == nil {
		w = os.Stderr
	}
	q.stderr = w
}

// now returns the timestamp to stamp on a new message.
func (q *Queue) now() time.Time {
	if q.clk == nil {
		return time.Now()
	}
	return q.clk.Now()
}

// topicFor returns the topic state, creating it on first use. The caller must
// hold at least the read lock, which is upgraded when a topic is missing.
func (q *Queue) topicFor(name string) *topic {
	q.mu.RLock()
	t, ok := q.topics[name]
	q.mu.RUnlock()
	if ok {
		return t
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if t, ok := q.topics[name]; ok {
		return t
	}
	t = &topic{ch: make(chan queue.Message, q.buffer)}
	q.topics[name] = t
	return t
}

// Publish appends payload to topic. It fails with errs.Conflict once the queue
// is closed and never panics on a closed queue.
func (q *Queue) Publish(ctx context.Context, topicName string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.RLock()
	closed := q.closed
	q.mu.RUnlock()
	if closed {
		return errs.Conflict("queue is closed")
	}
	t := q.topicFor(topicName)

	msg := queue.Message{
		ID:        idgen.ULID(),
		Topic:     topicName,
		Payload:   append([]byte(nil), payload...),
		CreatedAt: q.now(),
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case t.ch <- msg:
		t.depth++
		return nil
	default:
		return errs.Conflict("queue topic " + topicName + " is full")
	}
}

// Consume processes up to batch messages from topicName, blocking at most wait
// for the first message. A handler error re-enqueues the message with
// Attempts+1 at the tail of the topic; once MaxAttempts is reached the message
// is dropped and reported on stderr.
//
// A message that failed earlier in this same call is not offered to the handler
// again: it is parked and put back on the topic when the call returns, so a
// single Consume call cannot burn every retry of a poisoned payload in one
// tight loop. processed counts the messages the handler actually saw; err is the
// last handler error.
func (q *Queue) Consume(ctx context.Context, topicName string, batch int, wait time.Duration, h queue.Handler) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	q.mu.RLock()
	closed := q.closed
	q.mu.RUnlock()
	if closed {
		return 0, errs.Conflict("queue is closed")
	}
	if batch <= 0 {
		batch = DefaultBatch
	}
	if h == nil {
		return 0, errs.InvalidRequest("handler is required")
	}
	t := q.topicFor(topicName)

	// Block up to wait for the first message.
	msg, ok := q.take(ctx, t, wait)
	if !ok {
		return 0, ctx.Err()
	}

	processed := 0
	var lastErr error
	failed := make(map[string]struct{})
	var parked []queue.Message

	for {
		if _, retry := failed[msg.ID]; retry {
			// Failed earlier in this call: hold it for the next call so one
			// Consume cannot burn every retry of a poisoned payload at once.
			parked = append(parked, msg)
		} else {
			processed++
			if err := h(ctx, msg); err != nil {
				lastErr = err
				msg.Attempts++
				if msg.Attempts >= MaxAttempts {
					q.drop(t, msg, err)
				} else {
					failed[msg.ID] = struct{}{}
					parked = append(parked, msg)
				}
			}
		}
		if processed >= batch {
			break
		}
		// Drain what is already buffered; do not wait again.
		next, ok := q.take(ctx, t, 0)
		if !ok {
			break
		}
		msg = next
	}

	for _, m := range parked {
		q.requeue(t, m)
	}
	return processed, lastErr
}

// drop reports a message that exhausted its retries.
func (q *Queue) drop(t *topic, msg queue.Message, cause error) {
	w := q.stderrWriter()
	if w == nil {
		return
	}
	fmt.Fprintf(w, "memqueue: dropping message %s on topic %q after %d attempts: %v\n",
		msg.ID, msg.Topic, msg.Attempts, cause)
}

// stderrWriter returns the current drop-notice writer, guarded by the queue
// lock so SetStderr may run concurrently.
func (q *Queue) stderrWriter() io.Writer {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.stderr
}

// take receives one message, blocking at most wait. ok=false means nothing was
// available (or ctx ended).
func (q *Queue) take(ctx context.Context, t *topic, wait time.Duration) (queue.Message, bool) {
	if wait <= 0 {
		select {
		case msg := <-t.ch:
			t.decDepth()
			return msg, true
		case <-ctx.Done():
			return queue.Message{}, false
		default:
			return queue.Message{}, false
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case msg := <-t.ch:
		t.decDepth()
		return msg, true
	case <-timer.C:
		return queue.Message{}, false
	case <-ctx.Done():
		return queue.Message{}, false
	}
}

// requeue pushes a message back onto the tail of its topic. If the channel is
// full the message is dropped with a notice rather than blocking the consumer
// forever.
func (q *Queue) requeue(t *topic, msg queue.Message) {
	t.mu.Lock()
	select {
	case t.ch <- msg:
		t.depth++
		t.mu.Unlock()
		return
	default:
		t.mu.Unlock()
	}
	if w := q.stderrWriter(); w != nil {
		fmt.Fprintf(w, "memqueue: dropping message %s on topic %q: buffer full on retry\n",
			msg.ID, msg.Topic)
	}
}

// decDepth decrements the pending counter of t.
func (t *topic) decDepth() {
	t.mu.Lock()
	if t.depth > 0 {
		t.depth--
	}
	t.mu.Unlock()
}

// Depth reports the pending message count for a topic.
func (q *Queue) Depth(ctx context.Context, topicName string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	q.mu.RLock()
	t, ok := q.topics[topicName]
	q.mu.RUnlock()
	if !ok {
		return 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.depth, nil
}

// Ping always succeeds: the memory driver needs no backend.
func (q *Queue) Ping(ctx context.Context) error { return ctx.Err() }

// Close marks the queue closed. It is idempotent; afterwards Publish and
// Consume return an error instead of panicking, and already buffered messages
// stay readable through Depth.
func (q *Queue) Close() error {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
	})
	return nil
}
