// Package queue provides a Redpanda (Kafka-compatible) producer and consumer
// for inter-service communication in CareerScout.
package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"
)

// Topic constants — single source of truth for all topic names.
const (
	TopicURLsToProcess  = "urls.to_process"
	TopicTier1Queue     = "urls.tier1_queue"
	TopicTier2Queue     = "urls.tier2_queue"
	TopicTier3Queue     = "urls.tier3_queue"
	TopicAPIsDiscovered = "apis.discovered"
	TopicAPIsFailed     = "apis.failed"
	TopicJobsRaw        = "jobs.raw"
)

// Producer wraps a franz-go client for publishing records to Redpanda topics.
type Producer struct {
	client *kgo.Client
	log    *zap.Logger
}

// NewProducer creates a new Redpanda producer.
// brokers is a comma-separated list of broker addresses, e.g. "localhost:19092".
func NewProducer(ctx context.Context, brokers []string, log *zap.Logger) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchMaxBytes(1_000_000), // 1 MB max batch
		kgo.RecordDeliveryTimeout(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("queue: create producer: %w", err)
	}

	// Verify connectivity
	if err := client.Ping(ctx); err != nil {
		return nil, fmt.Errorf("queue: ping brokers: %w", err)
	}

	return &Producer{client: client, log: log}, nil
}

// Produce publishes a single record to the given topic.
// key is used for partition routing (typically the domain name).
func (p *Producer) Produce(ctx context.Context, topic, key string, value []byte) error {
	rec := &kgo.Record{
		Topic: topic,
		Key:   []byte(key),
		Value: value,
	}

	if err := p.client.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("queue: produce to %q: %w", topic, err)
	}

	p.log.Debug("produced record",
		zap.String("topic", topic),
		zap.String("key", key),
		zap.Int("value_bytes", len(value)),
	)

	return nil
}

// Close gracefully flushes pending records and closes the client.
func (p *Producer) Close() {
	p.client.Flush(context.Background()) //nolint:errcheck
	p.client.Close()
}

// Consumer wraps a franz-go client for consuming records from Redpanda topics.
type Consumer struct {
	client *kgo.Client
	log    *zap.Logger
}

// NewConsumer creates a new Redpanda consumer subscribed to the given topics.
func NewConsumer(ctx context.Context, brokers []string, groupID string, topics []string, log *zap.Logger) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(groupID),
		kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(),
		kgo.FetchMaxWait(500*time.Millisecond),
		kgo.FetchMaxBytes(10_000_000), // 10 MB
	)
	if err != nil {
		return nil, fmt.Errorf("queue: create consumer: %w", err)
	}

	return &Consumer{client: client, log: log}, nil
}

// Poll fetches the next batch of records. Blocks until records are available
// or the context is cancelled. Offsets are committed only after the handler
// returns nil to ensure at-least-once delivery.
func (c *Consumer) Poll(ctx context.Context, handler func(topic, key string, value []byte) error) error {
	fetches := c.client.PollFetches(ctx)

	if errs := fetches.Errors(); len(errs) > 0 {
		for _, e := range errs {
			c.log.Error("fetch error", zap.Error(e.Err), zap.String("topic", e.Topic))
		}
		return fmt.Errorf("queue: poll: %v", errs[0].Err)
	}

	var processErr error
	fetches.EachRecord(func(r *kgo.Record) {
		if processErr != nil {
			return // stop processing this batch on first error
		}
		if err := handler(r.Topic, string(r.Key), r.Value); err != nil {
			c.log.Error("handler failed",
				zap.String("topic", r.Topic),
				zap.String("key", string(r.Key)),
				zap.Error(err),
			)
			processErr = err
		}
	})

	if processErr != nil {
		return processErr
	}

	// Commit offsets only after all records in the batch are successfully handled.
	if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
		return fmt.Errorf("queue: commit offsets: %w", err)
	}

	return nil
}

// Close gracefully shuts down the consumer.
func (c *Consumer) Close() {
	c.client.Close()
}
