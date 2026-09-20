"""PaddleOCR integration and OCR quality checks for textbook pages.

PaddleOCR is imported lazily so command help and unit tests do not need to load
the native inference runtime. Model files are kept inside the project instead
of being written to the current user's home directory.
"""
from __future__ import annotations

import os
import re
import struct
from pathlib import Path
from typing import Any, Sequence


PROJECT_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_CACHE_DIR = PROJECT_ROOT / ".local" / "paddlex"
DEFAULT_MIN_CONFIDENCE = 0.45
DEFAULT_REVIEW_CONFIDENCE = 0.80


def normalized(text: str) -> str:
    return re.sub(r"[^a-z0-9]", "", text.lower())


def text_layer_is_reliable(rows: list[dict]) -> bool:
    """Reject empty or obviously fragmented PDF text layers.

    A usable embedded text layer is more accurate than OCR and preserves exact
    coordinates. Scans and damaged font maps fall through to PaddleOCR.
    """
    text = " ".join(str(row.get("text", "")) for row in rows)
    compact = re.sub(r"\s+", "", text)
    if len(normalized(text)) < 40 or "\ufffd" in text:
        return False
    visible = [character for character in compact if not character.isspace()]
    if not visible:
        return False
    letters = sum(character.isalpha() for character in visible)
    if letters / len(visible) < 0.55:
        return False
    tokens = re.findall(r"[A-Za-z]+(?:['\u2019-][A-Za-z]+)*", text)
    if len(tokens) < 6:
        return False
    suspicious_singles = sum(
        len(token) == 1 and token.lower() not in {"a", "i"} for token in tokens
    )
    return suspicious_singles / len(tokens) <= 0.25


def png_size(image: Path) -> tuple[int, int]:
    raw = image.read_bytes()[:24]
    if not raw.startswith(b"\x89PNG") or len(raw) < 24:
        raise ValueError(f"not a valid PNG: {image}")
    return struct.unpack(">II", raw[16:24])


def _list(value: Any) -> list:
    if value is None:
        return []
    if hasattr(value, "tolist"):
        value = value.tolist()
    return value if isinstance(value, list) else list(value)


def _rectangle(box: Sequence[Any], width: int, height: int) -> list[float] | None:
    values = _list(box)
    if len(values) == 4 and all(isinstance(value, (int, float)) for value in values):
        left, top, right, bottom = (float(value) for value in values)
    elif values and all(isinstance(point, (list, tuple)) and len(point) >= 2 for point in values):
        xs = [float(point[0]) for point in values]
        ys = [float(point[1]) for point in values]
        left, top, right, bottom = min(xs), min(ys), max(xs), max(ys)
    else:
        return None
    left, top = max(0.0, left), max(0.0, top)
    right, bottom = min(float(width), right), min(float(height), bottom)
    if right <= left or bottom <= top:
        return None
    return [left / width, top / height, (right - left) / width, (bottom - top) / height]


def _split_line_words(text: str, line_box: Sequence[float]) -> list[tuple[str, list[float]]]:
    """Approximate word boxes only when Paddle does not return word boxes."""
    matches = list(re.finditer(r"\S+", text))
    if not matches:
        return []
    left, top, box_width, box_height = (float(value) for value in line_box)
    length = max(1, len(text))
    return [
        (
            match.group(),
            [
                left + box_width * match.start() / length,
                top,
                box_width * max(1, match.end() - match.start()) / length,
                box_height,
            ],
        )
        for match in matches
    ]


def paddle_result_rows(
    result: Any,
    image_width: int,
    image_height: int,
    review_confidence: float = DEFAULT_REVIEW_CONFIDENCE,
) -> list[dict]:
    """Convert one PaddleOCR result into the project's row/word schema."""
    payload = result.json if hasattr(result, "json") else result
    if callable(payload):
        payload = payload()
    if not isinstance(payload, dict):
        return []
    data = payload.get("res", payload)
    texts = _list(data.get("rec_texts"))
    scores = _list(data.get("rec_scores"))
    line_boxes = _list(data.get("rec_boxes"))
    word_texts = _list(data.get("text_word"))
    word_boxes = _list(data.get("text_word_boxes"))
    rows: list[dict] = []
    for index, source_text in enumerate(texts):
        text = str(source_text).strip()
        if not text:
            continue
        score = float(scores[index]) if index < len(scores) else 0.0
        line_box = _rectangle(line_boxes[index], image_width, image_height) if index < len(line_boxes) else None
        words: list[dict] = []
        if index < len(word_texts) and index < len(word_boxes):
            contents = _list(word_texts[index])
            boxes = _list(word_boxes[index])
            for content, box in zip(contents, boxes):
                normalized_box = _rectangle(box, image_width, image_height)
                clean = str(content).strip()
                if clean and normalized_box:
                    words.append({
                        "text": clean,
                        "confidence": round(score, 6),
                        "needs_review": score < review_confidence,
                        "box": normalized_box,
                    })
        if not words and line_box:
            words = [
                {
                    "text": content,
                    "confidence": round(score, 6),
                    "needs_review": score < review_confidence,
                    "box": box,
                }
                for content, box in _split_line_words(text, line_box)
            ]
        if words:
            rows.append({
                "text": " ".join(word["text"] for word in words),
                "confidence": round(score, 6),
                "needs_review": score < review_confidence,
                "box": line_box,
                "words": words,
            })
    return rows


class PaddleOCREngine:
    """One reusable high-accuracy PP-OCRv5 engine per conversion job."""

    def __init__(self) -> None:
        os.environ.setdefault("PADDLE_PDX_CACHE_HOME", str(DEFAULT_CACHE_DIR))
        os.environ.setdefault("PADDLE_PDX_DISABLE_MODEL_SOURCE_CHECK", "1")
        os.environ.setdefault("HF_HOME", str(PROJECT_ROOT / ".local" / "huggingface"))
        try:
            from paddleocr import PaddleOCR
        except ImportError as exc:
            raise RuntimeError(
                "PaddleOCR is not installed; run .venv/bin/pip install -r requirements-ocr.txt"
            ) from exc
        self.min_confidence = float(
            os.environ.get("PADDLE_OCR_MIN_CONFIDENCE", DEFAULT_MIN_CONFIDENCE)
        )
        self.review_confidence = float(
            os.environ.get("PADDLE_OCR_REVIEW_CONFIDENCE", DEFAULT_REVIEW_CONFIDENCE)
        )
        self.engine = PaddleOCR(
            text_detection_model_name=os.environ.get(
                "PADDLE_OCR_DETECTION_MODEL", "PP-OCRv6_medium_det"
            ),
            text_recognition_model_name=os.environ.get(
                "PADDLE_OCR_RECOGNITION_MODEL", "PP-OCRv6_medium_rec"
            ),
            use_doc_orientation_classify=True,
            use_doc_unwarping=False,
            use_textline_orientation=True,
            text_det_limit_side_len=int(
                os.environ.get("PADDLE_OCR_DETECTION_MAX_SIDE", "1920")
            ),
            text_det_limit_type="max",
            text_recognition_batch_size=8,
            textline_orientation_batch_size=8,
            text_rec_score_thresh=self.min_confidence,
            return_word_box=True,
            device=os.environ.get("PADDLE_OCR_DEVICE", "cpu"),
        )

    def rows(self, image: Path) -> list[dict]:
        width, height = png_size(image)
        rows: list[dict] = []
        for result in self.engine.predict(
            str(image),
            text_rec_score_thresh=self.min_confidence,
            return_word_box=True,
        ):
            rows.extend(
                paddle_result_rows(result, width, height, self.review_confidence)
            )
        return rows
