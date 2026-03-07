// Package normalise implements the data normalisation and storage service (Team 7).
// Consumes `jobs.raw` topic, uses LLM-assisted field mapping,
// deduplicates, and writes canonical job records to Postgres.
package normalise

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/careerscout/careerscout/internal/queue"
	"go.uber.org/zap"
)

// RawJobEnvelope is the message schema on the jobs.raw topic.
type RawJobEnvelope struct {
	Domain     string          `json:"domain"`
	CompanyID  string          `json:"company_id"`
	RawJSON    json.RawMessage `json:"raw_json"`
	CapturedAt time.Time       `json:"captured_at"`
}

// Consumer reads from the jobs.raw Redpanda topic and dispatches each
// envelope to the normaliser pipeline.
type Consumer struct {
	consumer   *queue.Consumer
	normaliser *Normaliser
	writer     *Writer
	log        *zap.Logger
}

// NewConsumer creates a new jobs.raw consumer.
func NewConsumer(consumer *queue.Consumer, n *Normaliser, w *Writer, log *zap.Logger) *Consumer {
	return &Consumer{consumer: consumer, normaliser: n, writer: w, log: log}
}

// Run starts the consume loop. Blocks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.consumer.Poll(pollCtx, func(_, _ string, value []byte) error {
			return c.process(ctx, value)
		})
		cancel()

		if err != nil && ctx.Err() == nil {
			c.log.Warn("normalise: poll error", zap.Error(err))
			time.Sleep(1 * time.Second)
		}
	}
}

func (c *Consumer) process(ctx context.Context, value []byte) error {
	var envelope RawJobEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		return fmt.Errorf("normalise: unmarshal envelope: %w", err)
	}

	// Normalise the raw JSON into canonical job records
	jobs, err := c.normaliser.Normalise(ctx, envelope)
	if err != nil {
		c.log.Warn("normalise failed",
			zap.String("domain", envelope.Domain),
			zap.Error(err),
		)
		return nil // don't retry normalisation failures — they require human review
	}

	// Write to Postgres with deduplication
	if err := c.writer.Write(ctx, jobs); err != nil {
		return fmt.Errorf("normalise: write jobs: %w", err)
	}

	c.log.Info("normalised and stored jobs",
		zap.String("domain", envelope.Domain),
		zap.Int("job_count", len(jobs)),
	)
	return nil
}
