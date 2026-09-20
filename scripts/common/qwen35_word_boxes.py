"""Qwen3.5-OCR-only word-box enrichment.

Qwen's advanced recognition task returns a box for each text *line*.  This
module keeps Qwen as the text authority and uses an existing PaddleOCR result
only to attach word-level geometry.  It intentionally has no effect on the
standalone local-PaddleOCR pipeline or any other cloud model.
"""
from __future__ import annotations

import re
from typing import Any


Box = list[float]
_TOKEN = re.compile(r"\S+")
_NON_ALNUM = re.compile(r"[^a-z0-9]+")


def _box(value: object) -> Box | None:
    if not isinstance(value, list) or len(value) != 4:
        return None
    try:
        left, top, width, height = (float(item) for item in value)
    except (TypeError, ValueError):
        return None
    if width <= 0 or height <= 0:
        return None
    return [left, top, width, height]


def _normal(text: str) -> str:
    return _NON_ALNUM.sub("", text.lower().replace("’", "'"))


def _same_token(left: str, right: str) -> bool:
    normalized_left, normalized_right = _normal(left), _normal(right)
    if normalized_left and normalized_right:
        return normalized_left == normalized_right
    return left == right


def _line_tokens(text: str) -> list[str]:
    """Keep punctuation attached to its written word for one audio hotspot."""
    return _TOKEN.findall(text.strip())


def _candidate_words(rows: list[dict[str, Any]], line_box: Box) -> list[dict[str, Any]]:
    """Return Paddle words geometrically likely to belong to one Qwen line."""
    left, top, width, height = line_box
    right, bottom = left + width, top + height
    candidates: list[dict[str, Any]] = []
    for row in rows:
        for source in row.get("words", []):
            if not isinstance(source, dict):
                continue
            text = str(source.get("text") or "").strip()
            word_box = _box(source.get("box"))
            if not text or word_box is None:
                continue
            word_left, word_top, word_width, word_height = word_box
            word_right, word_bottom = word_left + word_width, word_top + word_height
            center_x, center_y = word_left + word_width / 2, word_top + word_height / 2
            vertical_overlap = max(0.0, min(bottom, word_bottom) - max(top, word_top))
            vertical_ratio = vertical_overlap / max(0.0001, min(height, word_height))
            vertically_near = abs(center_y - (top + height / 2)) <= max(height, word_height) * 0.85
            horizontally_inside = left - 0.02 <= center_x <= right + 0.02
            if horizontally_inside and (vertical_ratio >= 0.35 or vertically_near):
                candidates.append({
                    "text": text,
                    "box": word_box,
                    "confidence": source.get("confidence"),
                    "needs_review": bool(source.get("needs_review")),
                })
    return sorted(candidates, key=lambda item: (item["box"][0], item["box"][1]))


def _align_tokens(targets: list[str], candidates: list[dict[str, Any]]) -> dict[int, int]:
    """Align equal Qwen and Paddle tokens without allowing OCR text override."""
    count_target, count_candidate = len(targets), len(candidates)
    costs = [[0] * (count_candidate + 1) for _ in range(count_target + 1)]
    steps = [[""] * (count_candidate + 1) for _ in range(count_target + 1)]
    for index in range(1, count_target + 1):
        costs[index][0], steps[index][0] = index, "delete"
    for index in range(1, count_candidate + 1):
        costs[0][index], steps[0][index] = index, "insert"
    for target_index in range(1, count_target + 1):
        for candidate_index in range(1, count_candidate + 1):
            equal = _same_token(targets[target_index - 1], candidates[candidate_index - 1]["text"])
            choices = (
                (costs[target_index - 1][candidate_index - 1] + (0 if equal else 2), "match" if equal else "replace"),
                (costs[target_index - 1][candidate_index] + 1, "delete"),
                (costs[target_index][candidate_index - 1] + 1, "insert"),
            )
            costs[target_index][candidate_index], steps[target_index][candidate_index] = min(choices, key=lambda item: item[0])
    matches: dict[int, int] = {}
    target_index, candidate_index = count_target, count_candidate
    while target_index or candidate_index:
        step = steps[target_index][candidate_index]
        if step in {"match", "replace"}:
            if step == "match":
                matches[target_index - 1] = candidate_index - 1
            target_index, candidate_index = target_index - 1, candidate_index - 1
        elif step == "delete":
            target_index -= 1
        else:
            candidate_index -= 1
    return matches


def _estimated_boxes(tokens: list[str], line_box: Box) -> list[Box]:
    """Stable fallback when Paddle has no matching word geometry."""
    left, top, width, height = line_box
    weights = [max(1, len(_normal(token)) or len(token)) for token in tokens]
    total = max(1, sum(weights))
    offset = 0
    boxes: list[Box] = []
    for weight in weights:
        item_left = left + width * offset / total
        item_width = width * weight / total
        boxes.append([round(item_left, 6), round(top, 6), round(item_width, 6), round(height, 6)])
        offset += weight
    return boxes


def enrich_qwen35_rows(qwen_rows: list[dict[str, Any]], paddle_rows: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Convert Qwen line boxes into Qwen text with independently clickable words."""
    enriched: list[dict[str, Any]] = []
    total_words = matched_words = estimated_words = 0
    for row in qwen_rows:
        text = str(row.get("text") or "").strip()
        line_box = _box(row.get("box"))
        tokens = _line_tokens(text)
        if not text or line_box is None or not tokens:
            continue
        candidates = _candidate_words(paddle_rows, line_box)
        matches = _align_tokens(tokens, candidates)
        estimated = _estimated_boxes(tokens, line_box)
        words: list[dict[str, Any]] = []
        for index, token in enumerate(tokens):
            total_words += 1
            candidate = candidates[matches[index]] if index in matches else None
            if candidate is None:
                estimated_words += 1
                words.append({"text": token, "box": estimated[index], "confidence": 0.55, "needs_review": True})
                continue
            matched_words += 1
            confidence = candidate.get("confidence")
            words.append({
                "text": token,
                "box": [round(value, 6) for value in candidate["box"]],
                "confidence": float(confidence) if isinstance(confidence, (int, float)) else 1.0,
                "needs_review": bool(candidate.get("needs_review")),
            })
        enriched.append({
            **row,
            "text": text,
            "words": words,
            "needs_review": bool(row.get("needs_review")) or any(word["needs_review"] for word in words),
        })
    source = "local-paddleocr" if matched_words else "estimated"
    quality = {
        "word_box_source": source,
        "word_box_match_rate": round(matched_words / max(1, total_words), 4),
        "word_box_matched_count": matched_words,
        "estimated_word_count": estimated_words,
    }
    return enriched, quality
