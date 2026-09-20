"""Apple-Silicon Qwen3-TTS adapter used by the resident audio worker."""
from __future__ import annotations

import math
import os
import platform
import signal
import threading
import time
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

import numpy as np


DEFAULT_MODEL = "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit"

US_SENTENCE_INSTRUCT = """Use aiden's natural adult male voice.
Keep the speaker identity, timbre, vocal weight, and pitch range consistent.
Do not imitate a child, female speaker, or another speaker.
Speak in clear, natural General American English.
Use neutral textbook narration.
Pronounce every word clearly and accurately.
Use a moderate speaking speed.
Use a speaking speed appropriate for Chinese children aged 12 and under.
Do not add, omit, or repeat words.
End the audio cleanly."""

US_WORD_INSTRUCT = """Use aiden's natural adult male voice.
Keep exactly the same speaker identity and timbre.
Do not imitate a child, female speaker, or another speaker.
Pronounce only this English word once in clear General American English.
Use dictionary-quality pronunciation.
Use a speaking speed appropriate for Chinese children aged 12 and under.
Do not explain or repeat the word.
End immediately after pronunciation."""

UK_SENTENCE_INSTRUCT = """Speak in clear, natural Standard British English.
Use a neutral educated British accent similar to modern RP.
This is an English textbook for children.
Use natural sentence-level intonation.
Pronounce every word clearly and accurately.
Use a moderate speaking speed.
Use a speaking speed appropriate for Chinese children aged 12 and under.
Do not add words.
Do not omit words.
Do not repeat words or phrases.
Do not continue speaking after the sentence.
End the audio cleanly."""

UK_WORD_INSTRUCT = """Pronounce only this English word once in clear Standard British English.
Use a neutral modern British pronunciation suitable for an English learner.
Use a speaking speed appropriate for Chinese children aged 12 and under.
Do not explain the word.
Do not add any other words.
Do not repeat the word.
End immediately after pronunciation."""


def _env_float(name: str, default: float) -> float:
    try:
        return float(os.getenv(name, default))
    except ValueError:
        return default


def _env_bool(name: str, default: bool) -> bool:
    value = os.getenv(name)
    return default if value is None else value.strip().lower() not in {"0", "false", "no", "off"}


@dataclass(frozen=True)
class QwenTTSConfig:
    model: str = os.getenv("TTS_MODEL", DEFAULT_MODEL)
    american_speaker: str = os.getenv("TTS_AMERICAN_SPEAKER", "aiden")
    british_speaker: str = os.getenv("TTS_BRITISH_SPEAKER", "ryan")
    warmup: bool = _env_bool("TTS_WARMUP", True)
    max_word_seconds: float = _env_float("TTS_MAX_WORD_SECONDS", 8.0)
    max_sentence_seconds: float = _env_float("TTS_MAX_SENTENCE_SECONDS", 20.0)
    max_long_sentence_seconds: float = _env_float("TTS_MAX_LONG_SENTENCE_SECONDS", 45.0)
    generation_timeout: float = _env_float("TTS_GENERATION_TIMEOUT", 240.0)


@dataclass(frozen=True)
class SamplingProfile:
    temperature: float
    top_k: int
    top_p: float
    repetition_penalty: float


SAMPLING_PROFILE = SamplingProfile(
    temperature=0.65, top_k=40, top_p=0.85, repetition_penalty=1.1
)


_MODEL_LOCK = threading.Lock()
_SHARED_MODEL = None
_SHARED_MODEL_ID = ""
_SHARED_LOAD_SECONDS = 0.0


def _load_shared_model(model_id: str):
    global _SHARED_MODEL, _SHARED_MODEL_ID, _SHARED_LOAD_SECONDS
    with _MODEL_LOCK:
        if _SHARED_MODEL is not None:
            if _SHARED_MODEL_ID != model_id:
                raise RuntimeError(
                    f"TTS Worker 已加载 {_SHARED_MODEL_ID}，不能在同一进程再加载 {model_id}"
                )
            return _SHARED_MODEL, _SHARED_LOAD_SECONDS
        started = time.perf_counter()
        try:
            import mlx.core as mx
            from mlx_audio.tts.utils import load_model
        except (ImportError, RuntimeError) as exc:
            raise RuntimeError(
                "mlx-audio/Metal 初始化失败；请在 Apple Silicon 原生终端启动服务，并执行 "
                ".venv/bin/pip install -r requirements-audio.txt"
            ) from exc
        try:
            model = load_model(model_id)
            mx.eval(model.parameters())
        except Exception as exc:
            raise RuntimeError(
                f"Qwen3-TTS 模型加载失败：{model_id}；首次运行需联网下载，下载完成后可离线使用"
            ) from exc
        _SHARED_MODEL = model
        _SHARED_MODEL_ID = model_id
        _SHARED_LOAD_SECONDS = time.perf_counter() - started
        return _SHARED_MODEL, _SHARED_LOAD_SECONDS


def _trim_codec_padding(samples: np.ndarray, sample_rate: int) -> tuple[np.ndarray, dict]:
    """Remove only short codec edge padding; leave abnormal silence for QA."""
    value = np.asarray(samples, dtype=np.float32).reshape(-1)
    frame = max(1, int(sample_rate * 0.02))
    hop = max(1, int(sample_rate * 0.01))
    if sample_rate <= 0 or len(value) < frame:
        return value, {"leading_seconds": 0.0, "trailing_seconds": 0.0, "trimmed": False}
    peak = float(np.max(np.abs(value)))
    threshold = max(10 ** (-45 / 20), peak * 0.018)
    count = 1 + (len(value) - frame) // hop
    rms = np.empty(count, dtype=np.float32)
    for index in range(count):
        part = value[index * hop:index * hop + frame]
        rms[index] = math.sqrt(float(np.mean(part * part)))
    active = np.flatnonzero(rms > threshold)
    if not len(active):
        return value, {
            "leading_seconds": round(len(value) / sample_rate, 4),
            "trailing_seconds": round(len(value) / sample_rate, 4),
            "trimmed": False,
        }
    leading = int(active[0]) * hop / sample_rate
    trailing = max(0.0, (count - int(active[-1]) - 1) * hop / sample_rate)
    # Normal Qwen codec padding is below ~0.6 s. Anything materially longer
    # remains untouched so the existing silence/ASR gate rejects it.
    if leading > 0.75 or trailing > 0.75:
        return value, {"leading_seconds": round(leading, 4),
                       "trailing_seconds": round(trailing, 4), "trimmed": False}
    padding = int(sample_rate * 0.08)
    left = max(0, int(active[0]) * hop - padding)
    right = min(len(value), int(active[-1]) * hop + frame + padding)
    return value[left:right], {
        "leading_seconds": round(leading, 4),
        "trailing_seconds": round(trailing, 4),
        "trimmed": left > 0 or right < len(value),
        "removed_samples": left + (len(value) - right),
    }


@contextmanager
def _generation_deadline(seconds: float) -> Iterator[None]:
    """Interrupt a runaway Python generation loop when running on the main thread."""
    if seconds <= 0 or threading.current_thread() is not threading.main_thread():
        yield
        return
    previous_handler = signal.getsignal(signal.SIGALRM)

    def timeout_handler(_signum, _frame):
        raise TimeoutError(f"TTS generation exceeded {seconds:.0f} seconds")

    signal.signal(signal.SIGALRM, timeout_handler)
    signal.setitimer(signal.ITIMER_REAL, seconds)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)


class QwenTTSMLXEngine:
    """Small adapter around one process-wide mlx-audio Qwen3-TTS model."""

    def __init__(self, config: QwenTTSConfig | None = None):
        self.config = config or QwenTTSConfig()
        if platform.system() != "Darwin" or platform.machine() != "arm64":
            raise RuntimeError("Qwen3-TTS MLX 仅支持本项目目标环境：macOS Apple Silicon (arm64)")
        self.model, self.load_seconds = _load_shared_model(self.config.model)
        self.model_id = self.config.model
        try:
            supported = list(self.model.get_supported_speakers())
        except (AttributeError, TypeError) as exc:
            raise RuntimeError("当前 mlx-audio 模型不支持 get_supported_speakers()") from exc
        self.supported_speakers = supported
        self.american_speaker = self._resolve_speaker(self.config.american_speaker, "美式")
        self.british_speaker = self._resolve_speaker(self.config.british_speaker, "英式")
        try:
            from huggingface_hub.constants import HF_HUB_CACHE
            self.model_cache = Path(HF_HUB_CACHE)
        except ImportError:
            self.model_cache = Path.home() / ".cache" / "huggingface" / "hub"
        self.warmup_seconds = 0.0
        if self.config.warmup:
            started = time.perf_counter()
            self.synthesize("Hello.", "word", "en-US", attempt=1, warmup=True)
            self.warmup_seconds = time.perf_counter() - started

    def _resolve_speaker(self, configured: str, label: str) -> str:
        if configured in self.supported_speakers:
            return configured
        raise RuntimeError(
            f"{label} speaker {configured!r} 不在模型支持列表中：{self.supported_speakers}"
        )

    def speaker_for(self, accent: str) -> str:
        if accent == "en-US":
            return self.american_speaker
        if accent == "en-GB":
            return self.british_speaker
        raise ValueError(f"unsupported accent: {accent}")

    @staticmethod
    def memory_usage() -> dict:
        import mlx.core as mx
        return {
            "active_gb": round(float(mx.get_active_memory()) / 1e9, 4),
            "cache_gb": round(float(mx.get_cache_memory()) / 1e9, 4),
            "peak_gb": round(float(mx.get_peak_memory()) / 1e9, 4),
        }

    @staticmethod
    def instruct_for(kind: str, accent: str) -> str:
        if kind not in {"sentence", "word"}:
            raise ValueError(f"unsupported audio kind: {kind}")
        if accent == "en-US":
            return US_SENTENCE_INSTRUCT if kind == "sentence" else US_WORD_INSTRUCT
        if accent == "en-GB":
            return UK_SENTENCE_INSTRUCT if kind == "sentence" else UK_WORD_INSTRUCT
        raise ValueError(f"unsupported accent: {accent}")

    def maximum_duration(self, text: str, kind: str) -> float:
        if kind == "word":
            return self.config.max_word_seconds
        words = max(1, len(text.split()))
        estimated = max(self.config.max_sentence_seconds, words * 1.25 + 2.0)
        return min(self.config.max_long_sentence_seconds, estimated)

    def synthesize(self, text: str, kind: str, accent: str, attempt: int,
                   retry_variant: int = 0, warmup: bool = False,
                   speaker: str | None = None) -> tuple[np.ndarray, int, dict]:
        speaker = speaker or self.speaker_for(accent)
        if speaker not in self.supported_speakers:
            raise ValueError(f"unsupported Qwen3-TTS speaker: {speaker}")
        instruct = self.instruct_for(kind, accent)
        # Keep one sampling profile across words, sentences, and QA retries.
        # Varying temperature/top-k per item made the same preset speaker sound
        # noticeably different between clips.
        profile_index = 0
        profile = SAMPLING_PROFILE
        maximum_duration = self.maximum_duration(text, kind)
        # Qwen3-TTS uses a 12.5 Hz codec. This cap prevents a runaway decoder
        # from producing an unbounded word/sentence before waveform QA runs.
        max_tokens = max(32, int(math.ceil(maximum_duration * 12.5)) + 2)
        started = time.perf_counter()
        with _generation_deadline(self.config.generation_timeout):
            results = list(self.model.generate_custom_voice(
                text=text,
                speaker=speaker,
                language="English",
                instruct=instruct,
                temperature=profile.temperature,
                max_tokens=max_tokens,
                top_k=profile.top_k,
                top_p=profile.top_p,
                repetition_penalty=profile.repetition_penalty,
                verbose=False,
                stream=False,
            ))
        generation_seconds = time.perf_counter() - started
        if not results:
            raise RuntimeError("Qwen3-TTS returned no audio")
        sample_rates = {int(result.sample_rate) for result in results}
        if len(sample_rates) != 1:
            raise RuntimeError(f"Qwen3-TTS returned inconsistent sample rates: {sample_rates}")
        raw_samples = np.concatenate([
            np.asarray(result.audio, dtype=np.float32).reshape(-1) for result in results
        ])
        sample_rate = sample_rates.pop()
        samples, codec_padding = _trim_codec_padding(raw_samples, sample_rate)
        audio_duration = len(samples) / sample_rate if sample_rate else 0.0
        if not len(samples) or not np.isfinite(samples).all():
            raise RuntimeError("Qwen3-TTS returned empty or non-finite audio")
        if audio_duration > maximum_duration:
            raise RuntimeError(
                f"duration_invalid:{audio_duration:.2f}s exceeds {maximum_duration:.2f}s"
            )
        result_processing = sum(float(getattr(result, "processing_time_seconds", 0.0)) for result in results)
        peak_memory = max(float(getattr(result, "peak_memory_usage", 0.0)) for result in results)
        return samples, sample_rate, {
            "engine": "mlx-audio",
            "model": self.model_id,
            "language": "English",
            "speaker": speaker,
            "accent": accent,
            "instruct": instruct,
            "attempt_profile": profile_index + 1,
            "temperature": profile.temperature,
            "top_k": profile.top_k,
            "top_p": profile.top_p,
            "repetition_penalty": profile.repetition_penalty,
            "max_tokens": max_tokens,
            "maximum_duration": maximum_duration,
            "raw_audio_duration": round(len(raw_samples) / sample_rate, 4),
            "codec_padding": codec_padding,
            "generation_seconds": round(generation_seconds, 4),
            "model_processing_seconds": round(result_processing, 4),
            "audio_duration": round(audio_duration, 4),
            "rtf": round(generation_seconds / max(audio_duration, 1e-6), 4),
            "peak_memory_gb": round(peak_memory, 4),
            "warmup": warmup,
        }
