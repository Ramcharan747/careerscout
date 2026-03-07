//! scheduler.rs — Drives the main replay loop.
//!
//! Queries Postgres for companies whose `next_replay` timestamp has passed,
//! then dispatches each to the replayer with bounded concurrency.

use anyhow::Result;
use sqlx::PgPool;
use std::sync::Arc;
use tokio::sync::Semaphore;
use tokio::time::{interval, Duration};
use tracing::{info, warn};
use uuid::Uuid;

use crate::auth::AuthRefresher;
use crate::emitter::Emitter;
use crate::replayer::{Replayer, ReplayTarget};

const POLL_INTERVAL_SECS: u64 = 30;

pub struct Scheduler {
    db:          PgPool,
    http:        reqwest::Client,
    emitter:     Arc<Emitter>,
    concurrency: usize,
    batch_size:  i64,
}

impl Scheduler {
    pub fn new(
        db: PgPool,
        http: reqwest::Client,
        emitter: Emitter,
        concurrency: usize,
        batch_size: i64,
    ) -> Self {
        Self {
            db,
            http,
            emitter: Arc::new(emitter),
            concurrency,
            batch_size,
        }
    }

    /// Main event loop — polls Postgres every 30s for due replays.
    pub async fn run(self) -> Result<()> {
        let sem = Arc::new(Semaphore::new(self.concurrency));
        let mut tick = interval(Duration::from_secs(POLL_INTERVAL_SECS));

        loop {
            tick.tick().await;

            let targets = self.fetch_due_targets().await?;

            if targets.is_empty() {
                info!("No replay targets due, waiting...");
                continue;
            }

            info!(count = targets.len(), "Dispatching replay batch");

            for target in targets {
                let permit = Arc::clone(&sem).acquire_owned().await?;
                let http = self.http.clone();
                let emitter = Arc::clone(&self.emitter);
                let db = self.db.clone();

                tokio::spawn(async move {
                    let _permit = permit; // dropped when done

                    let replayer = Replayer::new(http.clone());
                    let auth = AuthRefresher::new(db.clone());

                    match replayer.replay(&target).await {
                        Ok(raw_json) => {
                            if let Err(e) = emitter.emit_jobs_raw(&target.domain, &target.company_id, raw_json).await {
                                warn!(domain = %target.domain, error = %e, "Failed to emit jobs.raw");
                            }
                            if let Err(e) = update_last_replayed(&db, &target.id).await {
                                warn!(domain = %target.domain, error = %e, "Failed to update last_replayed");
                            }
                        }
                        Err(e) => {
                            if e.to_string().contains("401") || e.to_string().contains("403") {
                                // Auth token expired — trigger Tier 2 re-discovery
                                if let Err(ae) = auth.trigger_rediscovery(&target.domain).await {
                                    warn!(domain = %target.domain, error = %ae, "Auth refresh trigger failed");
                                }
                            } else {
                                warn!(domain = %target.domain, error = %e, "Replay failed");
                            }
                        }
                    }
                });
            }
        }
    }

    /// Fetches the next batch of companies whose next_replay <= NOW().
    async fn fetch_due_targets(&self) -> Result<Vec<ReplayTarget>> {
        let rows = sqlx::query_as!(
            ReplayTarget,
            r#"
            SELECT
                dr.id,
                dr.company_id,
                dr.domain,
                dr.api_url   AS api_url,
                dr.http_method   AS http_method,
                dr.request_headers AS "request_headers: serde_json::Value",
                dr.request_body  AS request_body,
                dr.tier_used::TEXT AS tier_used
            FROM discovery_records dr
            WHERE
                dr.status = 'discovered'
                AND (dr.next_replay IS NULL OR dr.next_replay <= NOW())
            ORDER BY dr.next_replay ASC NULLS FIRST
            LIMIT $1
            "#,
            self.batch_size,
        )
        .fetch_all(&self.db)
        .await?;

        Ok(rows)
    }
}

async fn update_last_replayed(db: &PgPool, record_id: &Uuid) -> Result<()> {
    sqlx::query!(
        r#"
        UPDATE discovery_records
        SET
            last_replayed = NOW(),
            next_replay   = NOW() + INTERVAL '1 hour',
            consecutive_failures = 0
        WHERE id = $1
        "#,
        record_id,
    )
    .execute(db)
    .await?;
    Ok(())
}
