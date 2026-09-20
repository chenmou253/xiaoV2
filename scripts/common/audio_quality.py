"""Local waveform checks used by the textbook audio quality gate."""
from __future__ import annotations

import math
import os
from dataclasses import dataclass

import numpy as np


def _env_float(name: str, default: float) -> float:
    try:
        return float(os.getenv(name, default))
    except ValueError:
        return default


@dataclass(frozen=True)
class AudioQualityConfig:
    expected_sample_rate: int = int(os.getenv("AUDIO_EXPECTED_SAMPLE_RATE", "24000"))
    max_leading_silence: float = _env_float("AUDIO_MAX_LEADING_SILENCE", 0.35)
    max_trailing_silence: float = _env_float("AUDIO_MAX_TRAILING_SILENCE", 0.50)
    max_continuous_silence: float = _env_float("AUDIO_MAX_CONTINUOUS_SILENCE", 0.65)
    repeat_threshold: float = _env_float("AUDIO_REPEAT_THRESHOLD", 0.94)
    min_rms_db: float = _env_float("AUDIO_MIN_RMS_DB", -38.0)
    max_rms_db: float = _env_float("AUDIO_MAX_RMS_DB", -7.0)
    max_clipping_ratio: float = _env_float("AUDIO_MAX_CLIPPING_RATIO", 0.0005)


def _mono(samples: np.ndarray) -> np.ndarray:
    value = np.asarray(samples, dtype=np.float32)
    if value.ndim == 2:
        value = value.mean(axis=1)
    return value.reshape(-1)


def _frame_rms(samples: np.ndarray, sample_rate: int) -> tuple[np.ndarray, float]:
    frame = max(1, int(sample_rate * 0.02))
    hop = max(1, int(sample_rate * 0.01))
    if len(samples) < frame:
        rms = math.sqrt(float(np.mean(samples * samples))) if len(samples) else 0.0
        return np.asarray([rms], dtype=np.float32), len(samples) / sample_rate if sample_rate else 0.0
    count = 1 + (len(samples) - frame) // hop
    values = np.empty(count, dtype=np.float32)
    for index in range(count):
        part = samples[index * hop:index * hop + frame]
        values[index] = math.sqrt(float(np.mean(part * part)))
    return values, hop / sample_rate


def _silence_metrics(samples: np.ndarray, sample_rate: int) -> tuple[float, float, float, float]:
    rms, seconds_per_frame = _frame_rms(samples, sample_rate)
    peak = float(np.max(np.abs(samples))) if len(samples) else 0.0
    threshold = max(10 ** (-45 / 20), peak * 0.018)
    active = rms > threshold
    if not np.any(active):
        duration = len(samples) / sample_rate if sample_rate else 0.0
        return duration, duration, duration, threshold
    first, last = int(np.argmax(active)), len(active) - int(np.argmax(active[::-1])) - 1
    leading = first * seconds_per_frame
    trailing = max(0.0, (len(active) - last - 1) * seconds_per_frame)
    longest = current = 0
    for is_active in active:
        if is_active:
            longest = max(longest, current)
            current = 0
        else:
            current += 1
    longest = max(longest, current) * seconds_per_frame
    return leading, trailing, longest, threshold


def _correlation(left: np.ndarray, right: np.ndarray) -> float:
    size = min(len(left), len(right))
    if size < 32:
        return 0.0
    left = left[:size].astype(np.float64) - float(np.mean(left[:size]))
    right = right[:size].astype(np.float64) - float(np.mean(right[:size]))
    denominator = float(np.linalg.norm(left) * np.linalg.norm(right))
    return abs(float(np.dot(left, right) / denominator)) if denominator > 1e-10 else 0.0


def _spectrum_similarity(left: np.ndarray, right: np.ndarray) -> float:
    size = min(len(left), len(right))
    if size < 64:
        return 0.0
    window = np.hanning(size)
    a = np.log1p(np.abs(np.fft.rfft(left[:size] * window)))
    b = np.log1p(np.abs(np.fft.rfft(right[:size] * window)))
    denominator = float(np.linalg.norm(a) * np.linalg.norm(b))
    return float(np.dot(a, b) / denominator) if denominator > 1e-10 else 0.0


def detect_repetition(samples: np.ndarray, sample_rate: int, threshold: float = 0.94) -> dict:
    """Detect exact/near-exact loops, especially a repeated tail.

    Both waveform correlation and spectral similarity must agree. This avoids
    treating intentionally repeated words (for example ``very very``) as a
    loop merely because their spectra look alike.
    """
    samples = _mono(samples)
    best = {"detected": False, "waveform_similarity": 0.0,
            "spectrum_similarity": 0.0, "window_seconds": 0.0,
            "source_offset_seconds": 0.0, "target_offset_seconds": 0.0,
            "location": "none"}
    if sample_rate <= 0 or len(samples) < int(sample_rate * 0.45):
        return best
    for seconds in (0.25, 0.35, 0.50, 0.75, 1.0, 1.5):
        width = int(sample_rate * seconds)
        if len(samples) < width * 2:
            continue
        step = max(1, width // 4)
        # Catch an exact or near-exact loop anywhere in the file, not only at
        # the tail. Requiring agreement from both raw waveform correlation and
        # spectral similarity keeps naturally repeated words from triggering.
        for start in range(0, len(samples) - width * 2 + 1, step):
            candidate = samples[start:start + width]
            following = samples[start + width:start + width * 2]
            wave = _correlation(candidate, following)
            spectrum = _spectrum_similarity(candidate, following)
            if min(wave, spectrum) > min(best["waveform_similarity"], best["spectrum_similarity"]):
                best.update(waveform_similarity=round(wave, 6),
                            spectrum_similarity=round(spectrum, 6),
                            window_seconds=seconds,
                            source_offset_seconds=round(start / sample_rate, 4),
                            target_offset_seconds=round((start + width) / sample_rate, 4),
                            location="local")
            if wave >= threshold and spectrum >= max(0.97, threshold):
                best["detected"] = True
                return best
        tail = samples[-width:]
        # Search earlier non-overlapping windows; include the immediately
        # preceding block because generative TTS tail loops commonly occur there.
        for start in range(0, len(samples) - width * 2 + 1, step):
            candidate = samples[start:start + width]
            wave = _correlation(candidate, tail)
            spectrum = _spectrum_similarity(candidate, tail)
            combined = min(wave, spectrum)
            if combined > min(best["waveform_similarity"], best["spectrum_similarity"]):
                best.update(waveform_similarity=round(wave, 6),
                            spectrum_similarity=round(spectrum, 6),
                            window_seconds=seconds,
                            source_offset_seconds=round(start / sample_rate, 4),
                            target_offset_seconds=round((len(samples) - width) / sample_rate, 4),
                            location="tail")
            if wave >= threshold and spectrum >= max(0.97, threshold):
                best["detected"] = True
                return best
    return best


def analyze_audio(samples: np.ndarray, sample_rate: int, kind: str,
                  expected_text: str, config: AudioQualityConfig | None = None) -> dict:
    config = config or AudioQualityConfig()
    value = _mono(samples)
    reasons: list[str] = []
    finite = bool(len(value)) and bool(np.isfinite(value).all())
    if not finite:
        reasons.append("empty_or_non_finite_audio")
        value = np.nan_to_num(value)
    duration = len(value) / sample_rate if sample_rate > 0 else 0.0
    peak = float(np.max(np.abs(value))) if len(value) else 0.0
    rms = math.sqrt(float(np.mean(value * value))) if len(value) else 0.0
    rms_db = 20 * math.log10(max(rms, 1e-12))
    clipping_ratio = float(np.mean(np.abs(value) >= 0.999)) if len(value) else 0.0
    leading, trailing, longest_silence, silence_threshold = _silence_metrics(value, sample_rate)
    repetition = detect_repetition(value, sample_rate, config.repeat_threshold)
    word_count = max(1, len(expected_text.split()))
    minimum = 0.12 if kind == "word" else max(0.22, word_count * 0.09)
    maximum = 4.0 if kind == "word" else max(5.0, word_count * 1.25 + 2.0)
    if sample_rate != config.expected_sample_rate:
        reasons.append("unexpected_sample_rate")
    if duration < minimum:
        reasons.append("audio_too_short")
    if duration > maximum:
        reasons.append("audio_too_long")
    if peak < 0.015:
        reasons.append("near_silent_audio")
    if not config.min_rms_db <= rms_db <= config.max_rms_db:
        reasons.append("rms_out_of_range")
    if clipping_ratio > config.max_clipping_ratio:
        reasons.append("clipping_detected")
    if leading > config.max_leading_silence:
        reasons.append("leading_silence_too_long")
    if trailing > config.max_trailing_silence:
        reasons.append("trailing_silence_too_long")
    if longest_silence > config.max_continuous_silence:
        reasons.append("continuous_silence_too_long")
    if repetition["detected"]:
        reasons.append("tail_or_local_repetition")
    return {
        "passed": not reasons,
        "reasons": reasons,
        "duration": round(duration, 4),
        "sample_rate": sample_rate,
        "peak": round(peak, 6),
        "rms_db": round(rms_db, 3),
        "clipping_ratio": round(clipping_ratio, 7),
        "leading_silence": round(leading, 4),
        "trailing_silence": round(trailing, 4),
        "longest_silence": round(longest_silence, 4),
        "silence_threshold": round(silence_threshold, 7),
        "repetition_detected": repetition["detected"],
        "repetition": repetition,
    }
