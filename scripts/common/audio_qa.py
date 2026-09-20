"""Combined ASR, pronunciation alignment and waveform quality gate."""
from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from .audio_asr import FasterWhisperChecker
from .audio_pronunciation import check_pronunciation
from .audio_quality import AudioQualityConfig, analyze_audio


def _env_float(name: str, default: float) -> float:
    try:
        return float(os.getenv(name, default))
    except ValueError:
        return default


def _env_bool(name: str, default: bool) -> bool:
    value = os.getenv(name)
    return default if value is None else value.strip().lower() not in {"0", "false", "no", "off"}


@dataclass(frozen=True)
class AudioQAConfig:
    enabled: bool = _env_bool("AUDIO_QA_ENABLED", True)
    max_retry: int = max(1, int(os.getenv("AUDIO_MAX_RETRY", "3")))
    pass_wer: float = _env_float("AUDIO_PASS_WER", 0.02)
    max_wer: float = _env_float("AUDIO_MAX_WER", 0.05)
    max_cer: float = _env_float("AUDIO_MAX_CER", 0.08)
    min_pronunciation_score: float = _env_float("AUDIO_MIN_PRONUNCIATION_SCORE", 0.65)


class AudioQualityGate:
    def __init__(self, config: AudioQAConfig | None = None, cache_dir: Path | None = None):
        self.config = config or AudioQAConfig()
        self.waveform_config = AudioQualityConfig()
        self.asr = FasterWhisperChecker(cache_dir=cache_dir) if self.config.enabled else None

    def check(self, wav_path: Path, samples: np.ndarray, sample_rate: int,
              kind: str, expected: str, phonemes: str, timings: list[Any],
              attempt: int) -> dict:
        try:
            import soundfile as sf
            file_bytes = wav_path.stat().st_size
            if file_bytes < 128:
                raise ValueError(f"generated WAV is too small: {file_bytes} bytes")
            decoded, decoded_rate = sf.read(str(wav_path), dtype="float32", always_2d=False)
            decoded = np.asarray(decoded, dtype=np.float32)
            if decoded.ndim == 2:
                decoded = decoded.mean(axis=1)
            if decoded_rate != sample_rate or not len(decoded):
                raise ValueError("decoded WAV metadata does not match generated audio")
            samples = decoded
        except Exception as exc:
            return {
                "passed": False, "attempt": attempt,
                "asr": {"passed": False, "error": "wav_not_decodable"},
                "pronunciation": {"passed": False, "score": 0.0},
                "audio": {"passed": False, "reasons": ["damaged_wav"],
                          "error": f"{type(exc).__name__}: {exc}"},
                "final_score": 0.0, "reasons": ["damaged_wav"],
            }
        audio = analyze_audio(samples, sample_rate, kind, expected, self.waveform_config)
        audio["file_bytes"] = file_bytes
        if not self.config.enabled:
            return {
                "passed": audio["passed"], "attempt": attempt,
                "asr": {"enabled": False, "passed": True},
                "pronunciation": {"enabled": False, "passed": True, "score": 1.0},
                "audio": audio, "final_score": 1.0 if audio["passed"] else 0.0,
                "reasons": audio["reasons"],
            }
        asr = self.asr.check(wav_path, expected, kind)
        pronunciation = check_pronunciation(samples, sample_rate, expected, phonemes, timings, asr)
        pronunciation_passed = pronunciation["score"] >= self.config.min_pronunciation_score
        if kind == "sentence":
            homophone_or_tokenization_warning = (
                asr["cer"] <= self.config.max_cer
                and pronunciation["score"] >= max(0.85, self.config.min_pronunciation_score)
            )
            asr_passed = (
                asr["wer"] <= self.config.max_wer and asr["cer"] <= self.config.max_cer
            ) or homophone_or_tokenization_warning
            asr_warning = asr_passed and asr["wer"] > self.config.pass_wer
        else:
            # Whisper can be unstable on one-phoneme words. Exact ASR remains
            # preferred, while a strong phoneme alignment can rescue only a
            # very short token; waveform quality must still pass.
            very_short = len(asr["expected"].replace(" ", "")) <= 2
            expected_tokens = asr["expected"].split()
            recognized_tokens = asr["recognized"].split()
            contained = len(expected_tokens) == 1 and expected_tokens[0] in recognized_tokens and len(recognized_tokens) <= 3
            short_alignment = (
                very_short
                and bool(asr["recognized"])
                and pronunciation["timings_monotonic"]
                and pronunciation["timing_coverage"] >= 0.75
                and pronunciation["active_phoneme_ratio"] >= 0.50
                and pronunciation["score"] >= 0.45
            )
            asr_passed = asr["exact"] or (
                contained and pronunciation["score"] >= max(0.85, self.config.min_pronunciation_score)
            ) or (
                very_short and pronunciation["score"] >= max(0.68, self.config.min_pronunciation_score)
            ) or (
                short_alignment
            ) or (
                asr["cer"] <= 0.50
                and pronunciation["score"] >= max(0.68, self.config.min_pronunciation_score)
            )
            pronunciation_passed = pronunciation_passed or short_alignment
            asr_warning = asr_passed and not asr["exact"]
        asr["passed"] = asr_passed
        asr["warning"] = asr_warning
        pronunciation["passed"] = pronunciation_passed
        reasons = list(audio["reasons"])
        words = asr.get("words", [])
        if words and sample_rate > 0:
            duration = len(samples) / sample_rate
            asr_end = max(float(word.get("end", 0.0)) for word in words)
            allowance = 0.60 if kind == "word" else 0.90
            if duration - asr_end > allowance:
                tail = np.asarray(samples[int(min(duration, asr_end + 0.15) * sample_rate):], dtype=np.float32)
                overall_rms = float(np.sqrt(np.mean(np.asarray(samples, dtype=np.float32) ** 2)))
                tail_rms = float(np.sqrt(np.mean(tail ** 2))) if len(tail) else 0.0
                if tail_rms >= max(0.004, overall_rms * 0.22):
                    reasons.append("speech_tail_after_asr")
                    audio["speech_tail"] = {
                        "asr_end": round(asr_end, 4),
                        "remaining_seconds": round(duration - asr_end, 4),
                        "tail_rms": round(tail_rms, 6),
                    }
        if not asr_passed:
            reasons.append("asr_text_mismatch")
        if not pronunciation_passed:
            reasons.append("phoneme_alignment_low")
        final_score = (
            0.42 * max(0.0, 1.0 - min(1.0, float(asr["wer"])))
            + 0.33 * float(pronunciation["score"])
            + 0.25 * (1.0 if audio["passed"] else 0.0)
        )
        return {
            "passed": not reasons,
            "attempt": attempt,
            "asr": asr,
            "pronunciation": pronunciation,
            "audio": audio,
            "final_score": round(final_score, 6),
            "reasons": reasons,
        }
