"""
CareerScout ML Classifier — server.py
gRPC server exposing a single RPC: ClassifyRequest → ClassifyResponse.
Runs on CPU, <2ms per inference via ONNX Runtime.
"""
from __future__ import annotations

import logging
import os
import signal
import sys
import time
from concurrent import futures

import grpc

# These are generated from classifier.proto (run: python -m grpc_tools.protoc)
# Stubs are committed to the repo in classifier/proto/
sys.path.insert(0, str(__file__))

from model import Classifier

# Import generated gRPC stubs (generated at build time)
try:
    import sys
    import os
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), 'proto'))
    from proto import classifier_pb2, classifier_pb2_grpc
except ImportError:
    raise RuntimeError(
        "gRPC stubs not found. Run: "
        "python -m grpc_tools.protoc -I./proto --python_out=./proto "
        "--grpc_python_out=./proto ./proto/classifier.proto"
    )

# ── Configuration ─────────────────────────────────────────────────────────────

GRPC_PORT = int(os.getenv("GRPC_PORT", "50051"))
MAX_WORKERS = int(os.getenv("GRPC_WORKERS", "4"))
LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()

logging.basicConfig(
    level=getattr(logging, LOG_LEVEL, logging.INFO),
    format='{"time":"%(asctime)s","level":"%(levelname)s","msg":"%(message)s"}',
)
log = logging.getLogger("classifier")


# ── gRPC Service Implementation ───────────────────────────────────────────────

class ClassifierServicer(classifier_pb2_grpc.ClassifierServiceServicer):  # type: ignore[misc]
    """Implements the ClassifierService gRPC service."""

    def __init__(self):
        self._model = Classifier()
        log.info("Classifier model loaded")

    def Classify(self, request: classifier_pb2.ClassifyRequest, context: grpc.ServicerContext) -> classifier_pb2.ClassifyResponse:  # type: ignore[override]
        start = time.perf_counter()

        headers = dict(request.headers)
        is_jobs, confidence = self._model.predict(
            url=request.url,
            method=request.method,
            headers=headers,
            body=request.body,
        )

        latency_ms = (time.perf_counter() - start) * 1000

        log.debug(
            f"classify url={request.url!r} is_jobs={is_jobs} "
            f"confidence={confidence:.3f} latency_ms={latency_ms:.2f}"
        )

        return classifier_pb2.ClassifyResponse(
            is_jobs_api=is_jobs,
            confidence=confidence,
            reason=f"confidence={confidence:.3f}",
        )


# ── Server lifecycle ──────────────────────────────────────────────────────────

def serve():
    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=MAX_WORKERS),
        options=[
            ("grpc.max_receive_message_length", 4 * 1024 * 1024),  # 4 MB
            ("grpc.keepalive_time_ms", 30_000),
            ("grpc.keepalive_timeout_ms", 5_000),
        ],
    )

    classifier_pb2_grpc.add_ClassifierServiceServicer_to_server(
        ClassifierServicer(), server
    )

    listen_addr = f"[::]:{GRPC_PORT}"
    server.add_insecure_port(listen_addr)
    server.start()

    log.info(f"ML Classifier gRPC server listening on {listen_addr}")

    def _shutdown(signum, frame):
        log.info("Received shutdown signal, stopping server...")
        server.stop(grace=5).wait()
        sys.exit(0)

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    server.wait_for_termination()


if __name__ == "__main__":
    serve()

