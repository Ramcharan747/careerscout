//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/careerscout/careerscout/internal/tier2_v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://careerscout:careerscout_dev_password@localhost:5432/careerscout?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("skipping Postgres test; db connect failed: %v", err)
	}
	pool.Exec(ctx, "TRUNCATE discovery_records CASCADE")
	pool.Exec(ctx, "TRUNCATE companies CASCADE")
	return pool
}

func TestLoadCompanies_NoBatchSizeLimit(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	pool.Exec(ctx, "INSERT INTO companies (domain) VALUES ('test1.com'), ('test2.com'), ('test3.com')")

	companies, err := loadCompanies(ctx, pool, 0)
	if err != nil {
		t.Fatalf("loadCompanies failed: %v", err)
	}
	if len(companies) != 3 {
		t.Fatalf("expected 3 companies, got %d", len(companies))
	}
}

func TestLoadCompanies_BatchSizeRespected(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	pool.Exec(ctx, "INSERT INTO companies (domain) VALUES ('test1.com'), ('test2.com'), ('test3.com')")

	companies, err := loadCompanies(ctx, pool, 2)
	if err != nil {
		t.Fatalf("loadCompanies failed: %v", err)
	}
	if len(companies) != 2 {
		t.Fatalf("expected 2 companies due to limit, got %d", len(companies))
	}
}

func testResultWriterHelper(ctx context.Context, pool *pgxpool.Pool, minConf float64, co Company, result tier2_v3.CDPResult) error {
	if result.Confidence >= minConf {
		return saveDiscovered(ctx, pool, co, result)
	} else {
		return saveFailed(ctx, pool, co, "confidence below MIN_CONFIDENCE")
	}
}

func TestResultWriter_BelowMinConfidence_NotWritten(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	pool.Exec(ctx, "INSERT INTO companies (domain) VALUES ('submin.com')")
	var id string
	pool.QueryRow(ctx, "SELECT id FROM companies WHERE domain = 'submin.com'").Scan(&id)

	co := Company{ID: id, Domain: "submin.com"}
	res := tier2_v3.CDPResult{Success: true, APIURL: "http://api", Confidence: 0.50}

	err := testResultWriterHelper(ctx, pool, 0.54, co, res)
	if err != nil {
		t.Fatalf("helper failed: %v", err)
	}

	var status string
	err = pool.QueryRow(ctx, "SELECT status FROM discovery_records WHERE domain = 'submin.com'").Scan(&status)
	if err != nil {
		t.Fatalf("failed to query status: %v", err)
	}
	if status == "discovered" {
		t.Fatalf("expected status to be pending/failed, got discovered")
	}
}
