"""Faster-Whisper based transcription and text-consistency metrics."""
from __future__ import annotations

import os
import re
import unicodedata
from pathlib import Path


def normalize_asr_text(text: str) -> str:
    value = unicodedata.normalize("NFKC", str(text)).lower()
    value = value.replace("’", "'").replace("‘", "'")
    value = re.sub(r"_+|\*{2,}", " blank ", value)
    value = re.sub(r"[^a-z0-9']+", " ", value)
    return " ".join(value.split())


def _distance(left: list[str], right: list[str]) -> int:
    previous = list(range(len(right) + 1))
    for row, a in enumerate(left, 1):
        current = [row]
        for column, b in enumerate(right, 1):
            current.append(min(
                current[-1] + 1,
                previous[column] + 1,
                previous[column - 1] + (a != b),
            ))
        previous = current
    return previous[-1]


def error_rates(expected: str, recognized: str) -> tuple[float, float]:
    expected_normalized = normalize_asr_text(expected)
    recognized_normalized = normalize_asr_text(recognized)
    expected_words, recognized_words = expected_normalized.split(), recognized_normalized.split()
    wer = _distance(expected_words, recognized_words) / max(1, len(expected_words))
    expected_chars = list(expected_normalized.replace(" ", ""))
    recognized_chars = list(recognized_normalized.replace(" ", ""))
    cer = _distance(expected_chars, recognized_chars) / max(1, len(expected_chars))
    return round(wer, 6), round(cer, 6)


class FasterWhisperChecker:
    """Load one local Whisper model and reuse it for every item on the page."""

    def __init__(self, model_name: str | None = None, cache_dir: Path | None = None):
        try:
            from faster_whisper import WhisperModel
        except ImportError as exc:
            raise RuntimeError(
                "faster-whisper 未安装，请执行 .venv/bin/pip install -r requirements-audio.txt"
            ) from exc
        name = model_name or os.getenv("AUDIO_ASR_MODEL", "base.en")
        root = cache_dir or Path(os.getenv("AUDIO_ASR_CACHE", ".local/faster-whisper"))
        root.mkdir(parents=True, exist_ok=True)
        self.model_name = name
        self.model = WhisperModel(
            name,
            device="cpu",
            compute_type=os.getenv("AUDIO_ASR_COMPUTE_TYPE", "int8"),
            cpu_threads=max(1, int(os.getenv("AUDIO_ASR_THREADS", "4"))),
            download_root=str(root),
        )

    def check(self, wav_path: Path, expected: str, kind: str) -> dict:
        segments, info = self.model.transcribe(
            str(wav_path),
            language="en",
            beam_size=3 if kind == "sentence" else 1,
            best_of=1,
            temperature=0.0,
            condition_on_previous_text=False,
            word_timestamps=True,
            vad_filter=False,
        )
        recognized_parts: list[str] = []
        words: list[dict] = []
        avg_logprobs: list[float] = []
        no_speech: list[float] = []
        for segment in segments:
            recognized_parts.append(segment.text)
            avg_logprobs.append(float(segment.avg_logprob))
            no_speech.append(float(segment.no_speech_prob))
            for word in segment.words or []:
                words.append({
                    "word": normalize_asr_text(word.word),
                    "start": round(float(word.start), 4),
                    "end": round(float(word.end), 4),
                    "probability": round(float(word.probability), 6),
                })
        recognized = normalize_asr_text(" ".join(recognized_parts))
        normalized_expected = normalize_asr_text(expected)
        wer, cer = error_rates(normalized_expected, recognized)
        exact = recognized == normalized_expected
        return {
            "model": self.model_name,
            "expected": normalized_expected,
            "recognized": recognized,
            "wer": wer,
            "cer": cer,
            "exact": exact,
            "words": words,
            "language_probability": round(float(info.language_probability), 6),
            "avg_logprob": round(sum(avg_logprobs) / max(1, len(avg_logprobs)), 6),
            "no_speech_probability": round(sum(no_speech) / max(1, len(no_speech)), 6),
        }
