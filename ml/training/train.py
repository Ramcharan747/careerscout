"""
CareerScout ML — Training Pipeline (train.py)
Trains an XGBoost classifier on labelled API request data,
then exports the model to ONNX format for deployment.

Usage:
    python train.py --data data/labelled.jsonl --output ../classifier/model.onnx

Data format (JSONL, one record per line):
    {"url": "...", "method": "GET", "headers": {...}, "body": "...", "label": 1}
    label = 1 means is_jobs_api, 0 means not
"""
from __future__ import annotations

import argparse
import json
import logging
from pathlib import Path

import numpy as np
import xgboost as xgb
from skl2onnx import convert_sklearn
from skl2onnx.common.data_types import FloatTensorType
from sklearn.metrics import classification_report, roc_auc_score
from sklearn.model_selection import train_test_split

# Reuse the same featurize function from the classifier
import sys
sys.path.insert(0, str(Path(__file__).parent.parent / "classifier"))
from model import featurize  # noqa: E402

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("trainer")


def load_data(path: Path) -> tuple[np.ndarray, np.ndarray]:
    """Load JSONL labelled data and extract feature arrays."""
    X_rows, y_rows = [], []

    with path.open() as f:
        for i, line in enumerate(f):
            line = line.strip()
            if not line:
                continue
            try:
                record = json.loads(line)
                features = featurize(
                    url=record.get("url", ""),
                    method=record.get("method", "GET"),
                    headers=record.get("headers", {}),
                    body=record.get("body", ""),
                )
                X_rows.append(features.to_array().flatten())
                y_rows.append(int(record["label"]))
            except (KeyError, json.JSONDecodeError) as e:
                log.warning(f"Skipping line {i}: {e}")

    X = np.array(X_rows, dtype=np.float32)
    y = np.array(y_rows, dtype=np.int32)
    log.info(f"Loaded {len(y)} samples (positives: {y.sum()}, negatives: {len(y)-y.sum()})")
    return X, y


def train(X: np.ndarray, y: np.ndarray) -> xgb.XGBClassifier:
    """Train an XGBoost classifier."""
    # Class weights to handle imbalance (more non-job than job requests)
    pos = y.sum()
    neg = len(y) - pos
    scale_pos_weight = neg / max(pos, 1)

    model = xgb.XGBClassifier(
        n_estimators=200,
        max_depth=6,
        learning_rate=0.1,
        subsample=0.8,
        colsample_bytree=0.8,
        scale_pos_weight=scale_pos_weight,
        use_label_encoder=False,
        eval_metric="logloss",
        random_state=42,
    )

    X_train, X_val, y_train, y_val = train_test_split(
        X, y, test_size=0.2, random_state=42, stratify=y
    )

    model.fit(
        X_train, y_train,
        eval_set=[(X_val, y_val)],
        verbose=50,
    )

    # Evaluate on validation set
    y_pred = model.predict(X_val)
    y_proba = model.predict_proba(X_val)[:, 1]

    log.info("\n" + classification_report(y_val, y_pred, target_names=["not_jobs", "jobs"]))
    log.info(f"ROC-AUC: {roc_auc_score(y_val, y_proba):.4f}")

    return model


def export_onnx(model: xgb.XGBClassifier, output_path: Path, n_features: int) -> None:
    """Convert XGBoost model to ONNX format for runtime deployment."""
    initial_type = [("float_input", FloatTensorType([None, n_features]))]
    onnx_model = convert_sklearn(model, initial_types=initial_type)

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_bytes(onnx_model.SerializeToString())
    log.info(f"ONNX model written to {output_path} ({output_path.stat().st_size / 1024:.1f} KB)")


def main():
    parser = argparse.ArgumentParser(description="Train CareerScout ML Classifier")
    parser.add_argument("--data", type=Path, required=True, help="Path to labelled JSONL file")
    parser.add_argument("--output", type=Path, default=Path("../classifier/model.onnx"))
    args = parser.parse_args()

    X, y = load_data(args.data)
    model = train(X, y)
    export_onnx(model, args.output, n_features=X.shape[1])
    log.info("Training complete.")


if __name__ == "__main__":
    main()

