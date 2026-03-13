"""
CareerScout ML Classifier — model.py
Wraps an XGBoost model (serialised to ONNX format) for fast CPU inference.
The model classifies network requests as job API / not job API.

Feature engineering follows Section 4 of careerscout_foundation.md.
"""
from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional
from urllib.parse import urlparse

import numpy as np
import onnxruntime as rt


# ── Constants ─────────────────────────────────────────────────────────────────

MODEL_PATH = Path(__file__).parent / "model.onnx"

JOB_PATH_SEGMENTS = [
    "/jobs", "/careers", "/positions", "/openings", "/postings",
    "/vacancies", "/roles", "/graphql", "/api/v",
]

ATS_DOMAINS = [
    "greenhouse.io", "lever.co", "ashbyhq.com", "workday.com",
    "myworkdayjobs.com", "smartrecruiters.com", "bamboohr.com",
    "jobvite.com", "icims.com", "breezy.hr", "recruitee.com",
    "teamtailor.com", "personio.com",
]

BODY_KEYS = [
    "limit", "offset", "departments", "jobtype", "locationid",
    "operationname", "page", "size", "category", "team",
]

PAGINATION_PARAMS = ["page=", "from=", "size=", "offset=", "start=", "limit="]


# ── Feature extraction ────────────────────────────────────────────────────────

@dataclass
class Features:
    """All features fed to the XGBoost model."""
    # URL-derived
    has_job_path_segment: int = 0
    has_ats_domain: int = 0
    has_pagination_param: int = 0
    url_path_depth: int = 0
    is_graphql: int = 0

    # Method
    is_post: int = 0
    is_get: int = 0

    # Headers
    has_auth_bearer: int = 0
    has_api_key_header: int = 0
    has_json_content_type: int = 0
    has_accept_json: int = 0
    header_count: int = 0

    # Body
    has_job_body_key: int = 0
    body_length_bucket: int = 0  # 0=empty, 1=<100, 2=<1000, 3=1000+
    is_json_body: int = 0

    # Response content-type (if available)
    response_is_json: int = 0

    def to_array(self) -> np.ndarray:
        return np.array([
            self.has_job_path_segment,
            self.has_ats_domain,
            self.has_pagination_param,
            self.url_path_depth,
            self.is_graphql,
            self.is_post,
            self.is_get,
            self.has_auth_bearer,
            self.has_api_key_header,
            self.has_json_content_type,
            self.has_accept_json,
            self.header_count,
            self.has_job_body_key,
            self.body_length_bucket,
            self.is_json_body,
            self.response_is_json,
        ], dtype=np.float32).reshape(1, -1)


def featurize(
    url: str,
    method: str,
    headers: dict[str, str],
    body: str,
) -> Features:
    """Extract features from a raw network request."""
    f = Features()
    url_lower = url.lower()
    method_upper = method.upper()
    body_lower = body.lower()

    # Normalise headers to lowercase keys
    h = {k.lower(): v.lower() for k, v in headers.items()}

    # ── URL signals ──────────────────────────────────────────────────────
    try:
        parsed = urlparse(url)
        path_lower = parsed.path.lower()
        query_lower = (parsed.query or "").lower()
        f.url_path_depth = path_lower.count("/")
        f.is_graphql = int("graphql" in url_lower)

        f.has_job_path_segment = int(
            any(seg in path_lower for seg in JOB_PATH_SEGMENTS)
        )
        f.has_ats_domain = int(
            any(ats in url_lower for ats in ATS_DOMAINS)
        )
        f.has_pagination_param = int(
            any(p in query_lower for p in PAGINATION_PARAMS)
        )
    except Exception:
        pass

    # ── Method signals ───────────────────────────────────────────────────
    f.is_post = int(method_upper == "POST")
    f.is_get = int(method_upper == "GET")

    # ── Header signals ───────────────────────────────────────────────────
    f.header_count = min(len(h), 30)
    f.has_auth_bearer = int(
        "authorization" in h and h["authorization"].startswith("bearer ")
    )
    f.has_api_key_header = int(
        any(k in h for k in ["x-api-key", "x-auth-token", "api-key"])
    )
    f.has_json_content_type = int(
        "content-type" in h and "json" in h["content-type"]
    )
    f.has_accept_json = int(
        "accept" in h and "json" in h["accept"]
    )

    # ── Body signals ─────────────────────────────────────────────────────
    f.has_job_body_key = int(
        any(f'"{k}"' in body_lower for k in BODY_KEYS)
    )

    bl = len(body)
    if bl == 0:
        f.body_length_bucket = 0
    elif bl < 100:
        f.body_length_bucket = 1
    elif bl < 1000:
        f.body_length_bucket = 2
    else:
        f.body_length_bucket = 3

    # Check if body is valid JSON
    try:
        json.loads(body)
        f.is_json_body = 1
    except Exception:
        f.is_json_body = 0

    return f


# ── ONNX model wrapper ────────────────────────────────────────────────────────

class Classifier:
    """
    Loads and runs the ONNX XGBoost model for job API classification.
    Falls back to a rule-based heuristic if the model file is not found
    (useful during early development before the model is trained).
    """

    def __init__(self, model_path: Path = MODEL_PATH):
        self._session: Optional[rt.InferenceSession] = None
        self._input_name: Optional[str] = None

        if model_path.exists():
            opts = rt.SessionOptions()
            opts.intra_op_num_threads = 2
            opts.graph_optimization_level = rt.GraphOptimizationLevel.ORT_ENABLE_ALL
            self._session = rt.InferenceSession(
                str(model_path), sess_options=opts,
                providers=["CPUExecutionProvider"],
            )
            self._input_name = self._session.get_inputs()[0].name

    def predict(
        self,
        url: str,
        method: str,
        headers: dict[str, str],
        body: str,
    ) -> tuple[bool, float]:
        """
        Returns (is_jobs_api, confidence) for the given request.
        If the ONNX model is not loaded, uses a simple score heuristic.
        """
        features = featurize(url, method, headers, body)

        if self._session is not None:
            arr = features.to_array()
            result = self._session.run(None, {self._input_name: arr})
            # result[0][0] = predicted class (0 or 1)
            # result[1][0] = {"0": p_false, "1": p_true}
            label = int(result[0][0])
            proba = result[1][0].get("1", 0.5)
            return bool(label), float(proba)

        # Fallback: sum signals as raw score
        arr = features.to_array().flatten()
        score = float(arr.sum())
        confidence = min(score / 8.0, 1.0)
        return confidence >= 0.4, confidence

