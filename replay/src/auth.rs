//! auth.rs — Handles auth token expiry for the Replay Engine.
//! When a replay returns 401 or 403, the stored token is stale.
//! This module marks the company for re-discovery so Tier 2 captures a fresh token.

use anyhow::Result;
use sqlx::PgPool;
use tracing::info;

pub struct AuthRefresher {
    db: PgPool,
}

impl AuthRefresher {
    pub fn new(db: PgPool) -> Self {
        Self { db }
    }

    /// Mark the domain as stale and re-queue for Tier 2 re-discovery.
    /// The URL Ingestion service will pick it up on its next poll.
    pub async fn trigger_rediscovery(&self, domain: &str) -> Result<()> {
        sqlx::query!(
            r#"
            UPDATE discovery_records
            SET
                status      = 'stale',
                last_error  = 'auth_token_expired',
                next_replay = NOW() + INTERVAL '24 hours'
            WHERE domain = $1
            "#,
            domain,
        )
        .execute(&self.db)
        .await?;

        info!(domain = %domain, "Marked for re-discovery (auth token expired)");
        Ok(())
    }
}
