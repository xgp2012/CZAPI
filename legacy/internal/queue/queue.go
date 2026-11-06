// Package queue defines the async accounting transport (plan §6.1: Redis
// Stream first, swappable later). When the queue is unavailable the gateway
// degrades to synchronous processing so accounting is never lost.
package queue

import (
	"context"
	"time"
)

// Message is one accounting event.
type Message struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`
	Payload   []byte    `json:"payload"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
}

// Topics used by the gateway.
const (
	// TopicUsage carries a finished request's metering payload for the worker.
	TopicUsage = "usage"
	// TopicAudit carries administrative audit entries.
	TopicAudit = "audit"
)

// Handler processes a message. Returning an error schedules a retry.
type Handler func(ctx context.Context, m Message) error

// Queue is the publish/consume contract.
type Queue interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	// Consume processes up to batch messages, blocking at most wait.
	Consume(ctx context.Context, topic string, batch int, wait time.Duration, h Handler) (processed int, err error)
	// Depth reports the pending message count for a topic (for alerting).
	Depth(ctx context.Context, topic string) (int64, error)
	Ping(ctx context.Context) error
	Close() error
}
