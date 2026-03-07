//! emitter.rs — Writes raw job JSON to the `jobs.raw` Redpanda topic.

use anyhow::Result;
use rdkafka::config::ClientConfig;
use rdkafka::producer::{FutureProducer, FutureRecord};
use std::time::Duration;
use tracing::debug;
use uuid::Uuid;

const TOPIC_JOBS_RAW: &str = "jobs.raw";

pub struct Emitter {
    producer: FutureProducer,
}

impl Emitter {
    pub fn new(brokers: &[String]) -> Result<Self> {
        let producer: FutureProducer = ClientConfig::new()
            .set("bootstrap.servers", brokers.join(","))
            .set("message.timeout.ms", "30000")
            .set("acks", "all")
            .create()?;

        Ok(Self { producer })
    }

    /// Emit raw job JSON to jobs.raw topic.
    /// Key = domain for partition locality.
    pub async fn emit_jobs_raw(&self, domain: &str, company_id: &Uuid, raw_json: String) -> Result<()> {
        let envelope = serde_json::json!({
            "domain": domain,
            "company_id": company_id,
            "raw_json": raw_json,
            "captured_at": chrono::Utc::now().to_rfc3339(),
        });

        let payload = serde_json::to_string(&envelope)?;

        self.producer
            .send(
                FutureRecord::to(TOPIC_JOBS_RAW)
                    .key(domain)
                    .payload(&payload),
                Duration::from_secs(30),
            )
            .await
            .map_err(|(e, _)| anyhow::anyhow!("kafka produce error: {}", e))?;

        debug!(domain = %domain, "Emitted to jobs.raw");
        Ok(())
    }
}
