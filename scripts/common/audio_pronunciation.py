"""Word-timestamp and waveform alignment for local TTS quality checks."""
from __future__ import annotations

import math
from typing import Any

import numpy as np

from .audio_asr import normalize_asr_text


def _timing_value(timing: Any, name: str) -> float | str:
    if isinstance(timing, dict):
        return timing.get(name, 0)
    return getattr(timing, name, 0)


def _token_alignment(expected: str, recognized_words: list[dict]) -> tuple[float, list[dict]]:
    expected_words = normalize_asr_text(expected).split()
    recognized = [normalize_asr_text(item.get("word", "")) for item in recognized_words]
    rows, columns = len(expected_words) + 1, len(recognized) + 1
    costs = [[0] * columns for _ in range(rows)]
    path: list[list[tuple[int, int] | None]] = [[None] * columns for _ in range(rows)]
    for row in range(1, rows):
        costs[row][0], path[row][0] = row, (row - 1, 0)
    for column in range(1, columns):
        costs[0][column], path[0][column] = column, (0, column - 1)
    for row in range(1, rows):
        for column in range(1, columns):
            choices = [
                (costs[row - 1][column] + 1, (row - 1, column)),
                (costs[row][column - 1] + 1, (row, column - 1)),
                (costs[row - 1][column - 1] + (expected_words[row - 1] != recognized[column - 1]),
                 (row - 1, column - 1)),
            ]
            costs[row][column], path[row][column] = min(choices, key=lambda item: item[0])
    aligned: list[dict] = []
    row, column = len(expected_words), len(recognized)
    matched = 0
    while row or column:
        previous = path[row][column]
        if previous is None:
            break
        old_row, old_column = previous
        if old_row == row - 1 and old_column == column - 1 and row and column:
            ok = expected_words[row - 1] == recognized[column - 1]
            matched += int(ok)
            aligned.append({
                "expected": expected_words[row - 1],
                "recognized": recognized[column - 1],
                "start": recognized_words[column - 1].get("start"),
                "end": recognized_words[column - 1].get("end"),
                "matched": ok,
            })
        row, column = old_row, old_column
    aligned.reverse()
    return matched / max(1, len(expected_words)), aligned


def check_pronunciation(samples: np.ndarray, sample_rate: int, expected: str,
                        phonemes: str, timings: list[Any], asr: dict) -> dict:
    """Score alignment without assuming the TTS engine exposes phoneme timing.

    Qwen3-TTS currently returns waveform audio but no phoneme timestamps. Its
    path is checked with Whisper word timestamps, token alignment, confidence,
    and real waveform energy. A generic timed-phoneme path remains available
    for any future engine that supplies timings.
    """
    value = np.asarray(samples, dtype=np.float32).reshape(-1)
    duration = len(value) / sample_rate if sample_rate else 0.0
    word_coverage, word_alignment = _token_alignment(expected, asr.get("words", []))
    overall_rms = math.sqrt(float(np.mean(value * value))) if len(value) else 0.0

    if not timings:
        words = asr.get("words", [])
        starts = [float(item.get("start", 0.0)) for item in words]
        ends = [float(item.get("end", 0.0)) for item in words]
        monotonic = all(
            starts[index] >= ends[index - 1] - 0.05 and ends[index] >= starts[index]
            for index in range(1, len(words))
        ) and all(end <= duration + 0.15 for end in ends)
        timing_coverage = min(1.0, max(ends, default=0.0) / max(duration, 1e-6))
        confidence = sum(float(item.get("probability", 0.0)) for item in words) / max(1, len(words))
        energy_score = min(1.0, overall_rms / 0.025) if overall_rms > 0 else 0.0
        score = (
            0.70 * word_coverage
            + 0.20 * min(1.0, confidence / 0.75)
            + 0.10 * (energy_score if monotonic else energy_score * 0.35)
        )
        return {
            "method": "whisper-word-alignment+waveform-energy",
            "phonemes": "",
            "phoneme_count": 0,
            "active_phoneme_ratio": round(word_coverage, 6),
            "timing_coverage": round(timing_coverage, 6),
            "timings_monotonic": monotonic,
            "word_alignment_ratio": round(word_coverage, 6),
            "word_alignment": word_alignment,
            "mean_word_probability": round(confidence, 6),
            "waveform_rms": round(overall_rms, 6),
            "phoneme_intervals": [],
            "score": round(score, 6),
        }

    intervals: list[dict] = []
    active = 0
    valid = 0
    last_end = 0.0
    monotonic = True
    for timing in timings:
        phone = str(_timing_value(timing, "phoneme"))
        start = float(_timing_value(timing, "start"))
        end = float(_timing_value(timing, "end"))
        monotonic = monotonic and start >= last_end - 0.015 and end >= start and end <= duration + 0.12
        last_end = max(last_end, end)
        if not phone.strip() or phone in ".,!?;:—- 'ˈˌ":
            continue
        left, right = max(0, int(start * sample_rate)), min(len(value), int(end * sample_rate))
        part = value[left:right]
        rms = math.sqrt(float(np.mean(part * part))) if len(part) else 0.0
        is_active = len(part) >= max(8, int(sample_rate * 0.008)) and rms >= max(0.002, overall_rms * 0.12)
        valid += 1
        active += int(is_active)
        intervals.append({"phoneme": phone, "start": round(start, 4), "end": round(end, 4),
                          "rms": round(rms, 6), "active": is_active})
    phoneme_coverage = active / max(1, valid)
    timing_coverage = min(1.0, last_end / max(duration, 1e-6))
    duration_score = 1.0 if monotonic and 0.45 <= timing_coverage <= 1.05 else 0.35
    score = 0.45 * phoneme_coverage + 0.30 * word_coverage + 0.25 * duration_score
    return {
        "method": "whisper-word-alignment+timed-phoneme-energy",
        "phonemes": phonemes,
        "phoneme_count": valid,
        "active_phoneme_ratio": round(phoneme_coverage, 6),
        "timing_coverage": round(timing_coverage, 6),
        "timings_monotonic": monotonic,
        "word_alignment_ratio": round(word_coverage, 6),
        "word_alignment": word_alignment,
        "phoneme_intervals": intervals,
        "score": round(score, 6),
    }
