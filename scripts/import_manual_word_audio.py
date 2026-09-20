#!/usr/bin/env python3
"""Normalize a manually uploaded textbook word/phonics audio file to 24 kHz mono PCM16 WAV."""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import re
from pathlib import Path

import numpy as np
import soundfile as sf


def normalized_word(text: str) -> str:
    value = " ".join(text.strip().replace("‘", "'").replace("’", "'").split())
    value = re.sub(r"^[^\w]+|[^\w]+$", "", value, flags=re.UNICODE)
    return value.casefold() or text.strip().casefold()


def resample_linear(samples: np.ndarray, source_rate: int, target_rate: int) -> np.ndarray:
    if source_rate == target_rate:
        return samples.astype(np.float32, copy=False)
    if source_rate <= 0 or target_rate <= 0 or len(samples) == 0:
        raise ValueError("invalid sample rate")
    target_length = max(1, int(round(len(samples) * target_rate / source_rate)))
    source_x = np.linspace(0.0, 1.0, num=len(samples), endpoint=False, dtype=np.float64)
    target_x = np.linspace(0.0, 1.0, num=target_length, endpoint=False, dtype=np.float64)
    return np.interp(target_x, source_x, samples).astype(np.float32)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--text", required=True)
    args = parser.parse_args()

    source = Path(args.input)
    output = Path(args.output)
    if not source.is_file():
        raise ValueError("uploaded audio file does not exist")

    samples, sample_rate = sf.read(str(source), dtype="float32", always_2d=False)
    samples = np.asarray(samples, dtype=np.float32)
    if samples.ndim == 2:
        samples = samples.mean(axis=1)
    samples = samples.reshape(-1)
    if sample_rate < 8000:
        raise ValueError("sample rate is too low")
    if len(samples) == 0 or not np.isfinite(samples).all():
        raise ValueError("audio is empty or invalid")

    duration = len(samples) / sample_rate
    if duration < 0.05:
        raise ValueError("audio is too short")
    if duration > 8.0:
        raise ValueError("word audio must be 8 seconds or shorter")

    peak = float(np.max(np.abs(samples)))
    rms = math.sqrt(float(np.mean(samples * samples)))
    clipping_ratio = float(np.mean(np.abs(samples) >= 0.999))
    if peak < 0.002 or rms < 0.0001:
        raise ValueError("audio is silent or nearly silent")
    if clipping_ratio > 0.05:
        raise ValueError("audio has severe clipping")

    target_rate = 24000
    samples = resample_linear(samples, sample_rate, target_rate)
    output.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(output), samples, target_rate, subtype="PCM_16")

    data = output.read_bytes()
    normalized = normalized_word(args.text)
    cache_key = hashlib.sha256(normalized.encode("utf-8")).hexdigest()
    print(json.dumps({
        "normalized_word": normalized,
        "word_cache_key": cache_key,
        "sample_rate": target_rate,
        "duration_ms": int(round(len(samples) * 1000 / target_rate)),
        "file_size": len(data),
        "file_sha256": hashlib.sha256(data).hexdigest(),
        "peak": round(peak, 6),
        "rms": round(rms, 8),
        "clipping_ratio": round(clipping_ratio, 8),
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
