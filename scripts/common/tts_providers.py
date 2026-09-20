"""TTS provider adapters with explicit local/cloud retry semantics."""
from __future__ import annotations

import base64
import os
import tempfile
import threading
import time
from pathlib import Path
from typing import Protocol

from common.dashscope_client import DashScopeClient, DashScopeError
from common.qwen_tts_mlx import QwenTTSConfig, QwenTTSMLXEngine


LOCAL_QWEN_MODELS = {
    "local-qwen3-tts": os.getenv(
        "TTS_MODEL_06B",
        os.getenv("TTS_MODEL", "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit"),
    ),
    "local-qwen3-tts-1.7b": os.getenv(
        "TTS_MODEL_17B",
        "mlx-community/Qwen3-TTS-12Hz-1.7B-CustomVoice-8bit",
    ),
}

CLOUD_VOICES = {
    "Serena", "Ethan", "Chelsie", "Momo", "Vivian", "Moon", "Maia", "Kai",
    "Nofish", "Bella", "Jennifer", "Ryan", "Katerina", "Aiden", "Eldric Sage",
    "Mia", "Mochi", "Bellona", "Vincent", "Bunny", "Neil", "Elias", "Arthur",
    "Nini", "Seren", "Pip", "Stella", "Bodega", "Sonrisa", "Alek", "Dolce",
    "Sohee", "Ono Anna", "Lenn", "Emilien", "Andre", "Radio Gol",
}


class _CloudTTSRateLimiter:
    """Keep cloud TTS request starts below the configured per-second rate."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._next_allowed = 0.0

    def wait(self) -> None:
        try:
            requests_per_second = float(os.getenv("DASHSCOPE_TTS_RPS", "2"))
        except ValueError:
            requests_per_second = 2.0
        if requests_per_second <= 0:
            return
        interval = 1.0 / requests_per_second
        with self._lock:
            now = time.monotonic()
            delay = max(0.0, self._next_allowed - now)
            self._next_allowed = max(now, self._next_allowed) + interval
        if delay > 0:
            time.sleep(delay)


# The daemon reuses this limiter across all cloud provider instances and jobs.
_CLOUD_TTS_RATE_LIMITER = _CloudTTSRateLimiter()


class TTSProvider(Protocol):
    model_id: str
    cloud: bool
    retry_policy: str
    supported_speakers: set[str]

    def synthesize(self, text: str, kind: str, accent: str, attempt: int,
                   retry_variant: int, voice: str): ...

class LocalQwenTTSProvider:
    cloud = False
    retry_policy = "local-quality-gate"

    def __init__(self, model_id: str = "local-qwen3-tts") -> None:
        if model_id not in LOCAL_QWEN_MODELS:
            raise ValueError(f"unsupported local TTS model: {model_id}")
        self.model_id = model_id
        self.engine = QwenTTSMLXEngine(QwenTTSConfig(model=LOCAL_QWEN_MODELS[model_id]))
        self.supported_speakers = set(self.engine.supported_speakers)

    def synthesize(self, text: str, kind: str, accent: str, attempt: int,
                   retry_variant: int, voice: str):
        return self.engine.synthesize(
            text, kind, accent, attempt, retry_variant=retry_variant, speaker=voice,
        )

class QwenFlashTTSProvider:
    model_id = "qwen3-tts-flash"
    cloud = True
    retry_policy = "none"
    supported_speakers = CLOUD_VOICES

    def __init__(self, client: DashScopeClient | None = None) -> None:
        self.client = client or DashScopeClient()

    def synthesize(self, text: str, kind: str, accent: str, attempt: int,
                   retry_variant: int, voice: str):
        del kind, accent, retry_variant
        if attempt != 1:
            raise DashScopeError("cloud TTS automatic retry is disabled")
        if voice not in self.supported_speakers:
            raise ValueError(f"unsupported qwen3-tts-flash voice: {voice}")
        _CLOUD_TTS_RATE_LIMITER.wait()
        started = time.monotonic()
        body, request_id = self.client.generate({
            "model": self.model_id,
            "input": {"text": text, "voice": voice, "language_type": "English"},
        })
        try:
            audio = body["output"]["audio"]
        except (KeyError, TypeError) as exc:
            raise DashScopeError(f"Qwen TTS response is missing audio id={request_id or '-'}") from exc
        with tempfile.TemporaryDirectory(prefix="xiaov2-qwen-tts-") as directory:
            path = Path(directory) / "result.wav"
            if audio.get("data"):
                try:
                    path.write_bytes(base64.b64decode(audio["data"], validate=True))
                except (ValueError, TypeError) as exc:
                    raise DashScopeError(f"Qwen TTS returned invalid audio data id={request_id or '-'}") from exc
            elif audio.get("url"):
                self.client.download(str(audio["url"]), path)
            else:
                raise DashScopeError(f"Qwen TTS returned no audio id={request_id or '-'}")
            import soundfile as sf
            samples, sample_rate = sf.read(str(path), dtype="float32", always_2d=False)
        if getattr(samples, "ndim", 1) > 1:
            samples = samples.mean(axis=1)
        if len(samples) == 0 or sample_rate < 8000:
            raise DashScopeError(f"Qwen TTS returned invalid waveform id={request_id or '-'}")
        generation = {
            "provider": "dashscope",
            "request_id": request_id,
            "generation_seconds": round(time.monotonic() - started, 3),
            "audio_duration": round(len(samples) / sample_rate, 3),
        }
        return samples, sample_rate, generation

def create_tts_provider(model_id: str) -> TTSProvider:
    if model_id in LOCAL_QWEN_MODELS:
        return LocalQwenTTSProvider(model_id)
    if model_id == QwenFlashTTSProvider.model_id:
        return QwenFlashTTSProvider()
    raise ValueError(f"unsupported TTS model: {model_id}")
