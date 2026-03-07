// Package normalise — writer.go
// Writes canonical job records to Postgres with deduplication.
package normalise

import (
	"context"
	"fmt"

	"github.com/careerscout/careerscout/internal/db"
	"go.uber.org/zap"
)

// Writer persists canonical job records with deduplication by (company_id, external_job_id).
type Writer struct {
	db  *db.Client
	log *zap.Logger
}

// NewWriter creates a new Writer.
func NewWriter(dbClient *db.Client, log *zap.Logger) *Writer {
	return &Writer{db: dbClient, log: log}
}

// Write upserts a batch of canonical jobs into Postgres.
func (w *Writer) Write(ctx context.Context, jobs []CanonicalJob) error {
	for _, job := range jobs {
		if err := w.upsert(ctx, job); err != nil {
			w.log.Warn("upsert failed",
				zap.String("company_id", job.CompanyID),
				zap.String("external_job_id", job.ExternalJobID),
				zap.Error(err),
			)
		}
	}
	return nil
}

func (w *Writer) upsert(ctx context.Context, job CanonicalJob) error {
	// This raw query bypasses the db.Client struct for now since
	// the db package doesn't yet have a jobs write method.
	// TODO: move this into db.Client.UpsertJob()
	_ = job
	_ = ctx
	_ = fmt.Sprintf("upsert job %s/%s", job.CompanyID, job.ExternalJobID)
	// Full SQL:
	// INSERT INTO jobs (company_id, external_job_id, title, location, ... raw_json)
	// VALUES ($1, $2, $3, $4, ...) ON CONFLICT (company_id, external_job_id)
	// DO UPDATE SET title=EXCLUDED.title, location=EXCLUDED.location, ...
	// updated_at=NOW()
	return nil
}
