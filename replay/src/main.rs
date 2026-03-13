//! CareerScout Replay Engine — Rust
//!
//! This is the production scraping loop. After initial API discovery,
//! this service replays all captured API endpoints using stored headers and
//! tokens — no browser required at any point.
//!
//! Operating cost: ~$0.002 per 10,000 replays.
//!
//! Components:
//! - `scheduler` — determines which companies need a replay right now
//! - `replayer`  — fires raw HTTP requests using stored headers
//! - `auth`      — handles 401/403 by triggering Tier 2 re-discovery
//! - `emitter`   — writes raw job JSON to the `jobs.raw` Redpanda topic

use anyhow::Result;
use envconfig::Envconfig;
use tracing::{info, Level};
use tracing_subscriber::{fmt, EnvFilter};

mod auth;
mod emitter;
mod replayer;
mod scheduler;

#[derive(Envconfig)]
struct Config {
    #[envconfig(from = "DATABASE_URL")]
    database_url: String,

    #[envconfig(from = "REDPANDA_BROKERS", default = "localhost:19092")]
    redpanda_brokers: String,

    #[envconfig(from = "REPLAY_CONCURRENCY", default = "500")]
    concurrency: usize,

    #[envconfig(from = "REPLAY_BATCH_SIZE", default = "1000")]
    batch_size: i64,

    #[envconfig(from = "LOG_LEVEL", default = "info")]
    log_level: String,
}

#[tokio::main]
async fn main() -> Result<()> {
    // Initialise JSON structured logging
    let config = Config::init_from_env()?;

    let filter = EnvFilter::try_new(&config.log_level)
        .unwrap_or_else(|_| EnvFilter::new("info"));

    fmt()
        .json()
        .with_env_filter(filter)
        .with_target(false)
        .init();

    info!("CareerScout Replay Engine starting");
    info!(concurrency = config.concurrency, "worker pool size");

    // ── Database pool ─────────────────────────────────────────────────────────
    let db_pool = sqlx::postgres::PgPoolOptions::new()
        .max_connections(20)
        .connect(&config.database_url)
        .await?;
    info!("Connected to Postgres");

    // ── Redpanda emitter ──────────────────────────────────────────────────────
    let brokers: Vec<String> = config.redpanda_brokers
        .split(',')
        .map(str::trim)
        .map(String::from)
        .collect();

    let emitter = emitter::Emitter::new(&brokers)?;
    info!("Connected to Redpanda");

    // ── HTTP client — shared across all replay workers ────────────────────────
    let http_client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .gzip(true)
        .brotli(true)
        .deflate(true)
        .user_agent("Mozilla/5.0 (compatible; CareerScout/1.0)")
        .build()?;

    // ── Scheduler — drives the main replay loop ───────────────────────────────
    let scheduler = scheduler::Scheduler::new(
        db_pool.clone(),
        http_client,
        emitter,
        config.concurrency,
        config.batch_size,
    );

    // Graceful shutdown on SIGTERM / SIGINT
    let shutdown = tokio::signal::ctrl_c();
    tokio::select! {
        result = scheduler.run() => {
            if let Err(e) = result {
                tracing::error!(error = %e, "Scheduler terminated with error");
                std::process::exit(1);
            }
        }
        _ = shutdown => {
            info!("Received shutdown signal, draining...");
        }
    }

    info!("Replay Engine shut down cleanly");
    Ok(())
}

