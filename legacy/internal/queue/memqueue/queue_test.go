package memqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/queue"
)

var ctxBg = context.Background()

// --------------------------------------------------------------------------
// Publish / Consume
// --------------------------------------------------------------------------

func TestPublishConsume(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	for i := 0; i < 3; i++ {
		require.NoError(t, q.Publish(ctxBg, queue.TopicUsage, []byte(fmt.Sprintf("m%d", i))))
	}

	depth, err := q.Depth(ctxBg, queue.TopicUsage)
	require.NoError(t, err)
	assert.Equal(t, int64(3), depth)

	var got [][]byte
	processed, err := q.Consume(ctxBg, queue.TopicUsage, 10, time.Second, func(ctx context.Context, m queue.Message) error {
		assert.Equal(t, queue.TopicUsage, m.Topic)
		assert.NotEmpty(t, m.ID)
		assert.False(t, m.CreatedAt.IsZero())
		got = append(got, m.Payload)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, processed)
	assert.Equal(t, [][]byte{[]byte("m0"), []byte("m1"), []byte("m2")}, got, "FIFO order")

	depth, err = q.Depth(ctxBg, queue.TopicUsage)
	require.NoError(t, err)
	assert.Zero(t, depth)
}

func TestConsumeRespectsBatch(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	for i := 0; i < 5; i++ {
		require.NoError(t, q.Publish(ctxBg, queue.TopicUsage, []byte("x")))
	}

	processed, err := q.Consume(ctxBg, queue.TopicUsage, 2, time.Second, func(context.Context, queue.Message) error {
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, processed)

	depth, err := q.Depth(ctxBg, queue.TopicUsage)
	require.NoError(t, err)
	assert.Equal(t, int64(3), depth, "the rest stays queued")
}

func TestConsumeWaitsForMessage(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = q.Publish(ctxBg, "t", []byte("late"))
	}()

	start := time.Now()
	processed, err := q.Consume(ctxBg, "t", 1, time.Second, func(context.Context, queue.Message) error {
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Less(t, time.Since(start), time.Second)
}

func TestConsumeTimesOutOnEmptyTopic(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	start := time.Now()
	processed, err := q.Consume(ctxBg, "empty", 10, 50*time.Millisecond, func(context.Context, queue.Message) error {
		return nil
	})
	require.NoError(t, err)
	assert.Zero(t, processed)
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)
}

func TestConsumeStopsOnContextCancel(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	ctx, cancel := context.WithCancel(ctxBg)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := q.Consume(ctx, "t", 1, 5*time.Second, func(context.Context, queue.Message) error {
		return nil
	})
	require.Error(t, err)
}

func TestConsumeRejectsNilHandler(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()
	_, err := q.Consume(ctxBg, "t", 1, time.Millisecond, nil)
	require.Error(t, err)
}

func TestPayloadIsCopied(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	buf := []byte("original")
	require.NoError(t, q.Publish(ctxBg, "t", buf))
	buf[0] = 'X'

	_, err := q.Consume(ctxBg, "t", 1, time.Second, func(_ context.Context, m queue.Message) error {
		assert.Equal(t, []byte("original"), m.Payload)
		return nil
	})
	require.NoError(t, err)
}

// --------------------------------------------------------------------------
// Retry semantics
// --------------------------------------------------------------------------

func TestHandlerErrorRetriesThenDrops(t *testing.T) {
	q := New()
	var stderr bytes.Buffer
	q.SetStderr(&stderr)
	defer func() { require.NoError(t, q.Close()) }()

	require.NoError(t, q.Publish(ctxBg, queue.TopicUsage, []byte("poison")))

	attempts := 0
	boom := errors.New("boom")
	for i := 0; i < MaxAttempts+3; i++ {
		processed, err := q.Consume(ctxBg, queue.TopicUsage, 1, 20*time.Millisecond, func(_ context.Context, m queue.Message) error {
			attempts++
			assert.Equal(t, i, m.Attempts, "Attempts increments on every failure")
			return boom
		})
		if processed == 0 {
			break // the message was dropped: nothing left to consume
		}
		require.Error(t, err)
	}

	assert.Equal(t, MaxAttempts, attempts, "the message is tried MaxAttempts times")

	depth, err := q.Depth(ctxBg, queue.TopicUsage)
	require.NoError(t, err)
	assert.Zero(t, depth, "the poisoned message is dropped, not requeued forever")

	assert.Contains(t, stderr.String(), "dropping message")
	assert.Contains(t, stderr.String(), "boom")
}

func TestFailedMessageDoesNotBlockTheQueue(t *testing.T) {
	q := New()
	q.SetStderr(&bytes.Buffer{})
	defer func() { require.NoError(t, q.Close()) }()

	require.NoError(t, q.Publish(ctxBg, "t", []byte("bad")))
	require.NoError(t, q.Publish(ctxBg, "t", []byte("good")))

	var seen []string
	_, err := q.Consume(ctxBg, "t", 10, time.Second, func(_ context.Context, m queue.Message) error {
		seen = append(seen, string(m.Payload))
		if string(m.Payload) == "bad" {
			return errors.New("nope")
		}
		return nil
	})
	require.Error(t, err)
	assert.Equal(t, []string{"bad", "good"}, seen, "a failure is re-queued at the tail, not in front")

	depth, err := q.Depth(ctxBg, "t")
	require.NoError(t, err)
	assert.Equal(t, int64(1), depth, "the failed message waits for its retry")
}

func TestRetrySucceedsOnSecondAttempt(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	require.NoError(t, q.Publish(ctxBg, "t", []byte("flaky")))

	calls := 0
	_, err := q.Consume(ctxBg, "t", 1, time.Second, func(_ context.Context, m queue.Message) error {
		calls++
		return errors.New("transient")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)

	processed, err := q.Consume(ctxBg, "t", 1, time.Second, func(_ context.Context, m queue.Message) error {
		calls++
		assert.Equal(t, 1, m.Attempts)
		return nil
	})
	require.NoError(t, err, "a successful retry clears the error")
	assert.Equal(t, 1, processed)
	assert.Equal(t, 2, calls)

	depth, err := q.Depth(ctxBg, "t")
	require.NoError(t, err)
	assert.Zero(t, depth, "the delivered message is gone")
}

// --------------------------------------------------------------------------
// Depth, topics, lifecycle
// --------------------------------------------------------------------------

func TestDepthIsPerTopic(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	require.NoError(t, q.Publish(ctxBg, queue.TopicUsage, []byte("a")))
	require.NoError(t, q.Publish(ctxBg, queue.TopicAudit, []byte("b")))
	require.NoError(t, q.Publish(ctxBg, queue.TopicAudit, []byte("c")))

	usage, err := q.Depth(ctxBg, queue.TopicUsage)
	require.NoError(t, err)
	assert.Equal(t, int64(1), usage)

	audit, err := q.Depth(ctxBg, queue.TopicAudit)
	require.NoError(t, err)
	assert.Equal(t, int64(2), audit)

	unknown, err := q.Depth(ctxBg, "never-used")
	require.NoError(t, err)
	assert.Zero(t, unknown)
}

func TestTopicsAreIsolated(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	require.NoError(t, q.Publish(ctxBg, queue.TopicUsage, []byte("usage")))
	require.NoError(t, q.Publish(ctxBg, queue.TopicAudit, []byte("audit")))

	var seen []string
	_, err := q.Consume(ctxBg, queue.TopicAudit, 10, 20*time.Millisecond, func(_ context.Context, m queue.Message) error {
		seen = append(seen, string(m.Payload))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"audit"}, seen)

	usageDepth, err := q.Depth(ctxBg, queue.TopicUsage)
	require.NoError(t, err)
	assert.Equal(t, int64(1), usageDepth, "consuming one topic leaves the other alone")
}

func TestCloseIsIdempotentAndPublishFailsCleanly(t *testing.T) {
	q := New()
	require.NoError(t, q.Publish(ctxBg, "t", []byte("before")))

	require.NoError(t, q.Close())
	require.NoError(t, q.Close(), "Close is idempotent")

	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			assert.NoError(t, q.Close())
		}()
	}
	wg.Wait()

	// Publish after Close returns an error and must not panic on a closed
	// channel.
	err := q.Publish(ctxBg, "t", []byte("after"))
	require.Error(t, err)

	_, err = q.Consume(ctxBg, "t", 1, time.Millisecond, func(context.Context, queue.Message) error { return nil })
	require.Error(t, err)
}

func TestPingAndDefaults(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()
	require.NoError(t, q.Ping(ctxBg))

	assert.Equal(t, TopicBuffer, q.buffer)

	q2 := NewWithBuffer(0)
	defer func() { require.NoError(t, q2.Close()) }()
	assert.Equal(t, TopicBuffer, q2.buffer, "a non-positive capacity falls back to the default")

	q3 := NewWithBuffer(3)
	defer func() { require.NoError(t, q3.Close()) }()
	assert.Equal(t, 3, q3.buffer)
}

func TestPublishFailsWhenTopicIsFull(t *testing.T) {
	q := NewWithBuffer(2)
	defer func() { require.NoError(t, q.Close()) }()

	require.NoError(t, q.Publish(ctxBg, "small", []byte("1")))
	require.NoError(t, q.Publish(ctxBg, "small", []byte("2")))

	err := q.Publish(ctxBg, "small", []byte("3"))
	require.Error(t, err, "a full topic reports backpressure instead of blocking or panicking")

	depth, err := q.Depth(ctxBg, "small")
	require.NoError(t, err)
	assert.Equal(t, int64(2), depth)
}

func TestDefaultBatchWhenBatchNotPositive(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	for i := 0; i < 3; i++ {
		require.NoError(t, q.Publish(ctxBg, "t", []byte("x")))
	}
	processed, err := q.Consume(ctxBg, "t", 0, time.Second, func(context.Context, queue.Message) error {
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, processed, "batch<=0 means DefaultBatch")
}

func TestSetStderrNilRestoresDefault(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	var buf bytes.Buffer
	q.SetStderr(&buf)
	q.SetStderr(nil)

	// Drops now go to os.Stderr, not the buffer.
	require.NoError(t, q.Publish(ctxBg, "t", []byte("poison")))
	for i := 0; i < MaxAttempts; i++ {
		_, _ = q.Consume(ctxBg, "t", 1, 20*time.Millisecond, func(_ context.Context, m queue.Message) error {
			return errors.New("boom")
		})
	}
	assert.Empty(t, buf.String())
	assert.NotNil(t, q.stderrWriter())
}

// --------------------------------------------------------------------------
// Concurrency
// --------------------------------------------------------------------------

func TestConcurrentPublishAndConsume(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, q.Publish(ctxBg, "t", []byte(fmt.Sprint(i))))
		}(i)
	}
	wg.Wait()

	depth, err := q.Depth(ctxBg, "t")
	require.NoError(t, err)
	assert.Equal(t, int64(n), depth)

	var (
		mu   sync.Mutex
		seen = make(map[string]struct{}, n)
	)
	consumed := 0
	for consumed < n {
		got, err := q.Consume(ctxBg, "t", 25, time.Second, func(_ context.Context, m queue.Message) error {
			mu.Lock()
			seen[string(m.Payload)] = struct{}{}
			mu.Unlock()
			return nil
		})
		require.NoError(t, err)
		if got == 0 {
			break
		}
		consumed += got
	}
	assert.Equal(t, n, consumed)
	assert.Len(t, seen, n, "every message is delivered exactly once")
}

func TestConcurrentConsumersDoNotDoubleDeliver(t *testing.T) {
	q := New()
	defer func() { require.NoError(t, q.Close()) }()

	const n = 100
	for i := 0; i < n; i++ {
		require.NoError(t, q.Publish(ctxBg, "t", []byte(fmt.Sprint(i))))
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[string]int, n)
	)
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := q.Consume(ctxBg, "t", 7, 10*time.Millisecond, func(_ context.Context, m queue.Message) error {
					mu.Lock()
					seen[string(m.Payload)]++
					mu.Unlock()
					return nil
				})
				if err != nil || got == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()

	assert.Len(t, seen, n)
	for payload, count := range seen {
		assert.Equal(t, 1, count, "payload %s delivered %d times", payload, count)
	}
}
