//! replayer.rs — Fires raw HTTP replay requests using stored headers/tokens.
//! No browser involved. Sub-millisecond latency at scale.

use anyhow::{anyhow, Result};
use serde::Deserialize;
use uuid::Uuid;

/// Stored discovery record fetched from Postgres — the replay target.
#[derive(Debug, sqlx::FromRow, Deserialize)]
pub struct ReplayTarget {
    pub id:              Uuid,
    pub company_id:      Uuid,
    pub domain:          String,
    pub api_url:         Option<String>,
    pub http_method:     Option<String>,
    pub request_headers: Option<serde_json::Value>,
    pub request_body:    Option<String>,
    pub tier_used:       Option<String>,
}

/// Replayer fires a single raw HTTP request for one company's discovered API.
pub struct Replayer {
    client: reqwest::Client,
}

impl Replayer {
    pub fn new(client: reqwest::Client) -> Self {
        Self { client }
    }

    /// Replay sends the stored API request and returns the raw JSON response body.
    /// Returns Err if the response is non-2xx, with the status code in the error message.
    pub async fn replay(&self, target: &ReplayTarget) -> Result<String> {
        let url = target.api_url.as_deref()
            .ok_or_else(|| anyhow!("no api_url for domain {}", target.domain))?;

        let method = target.http_method.as_deref().unwrap_or("GET").to_uppercase();

        // Build the request
        let method_parsed = reqwest::Method::from_bytes(method.as_bytes())
            .map_err(|e| anyhow!("invalid method {}: {}", method, e))?;

        let mut req_builder = self.client.request(method_parsed, url);

        // Re-apply all stored headers
        if let Some(headers_val) = &target.request_headers {
            if let Some(obj) = headers_val.as_object() {
                for (k, v) in obj {
                    if let Some(v_str) = v.as_str() {
                        req_builder = req_builder.header(k.as_str(), v_str);
                    }
                }
            }
        }

        // Re-apply stored POST body
        if let Some(body) = &target.request_body {
            if !body.is_empty() {
                req_builder = req_builder.body(body.clone());
            }
        }

        let resp = req_builder.send().await?;
        let status = resp.status();

        if !status.is_success() {
            return Err(anyhow!(
                "non-2xx response {} for {}",
                status.as_u16(),
                target.domain
            ));
        }

        let body = resp.text().await?;

        // Basic sanity check — should look like JSON
        if !body.trim_start().starts_with('{') && !body.trim_start().starts_with('[') {
            return Err(anyhow!("response from {} does not look like JSON", target.domain));
        }

        Ok(body)
    }
}

