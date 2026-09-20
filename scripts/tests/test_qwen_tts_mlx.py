from __future__ import annotations

import unittest
from types import SimpleNamespace

import numpy as np

from common.qwen_tts_mlx import QwenTTSConfig, QwenTTSMLXEngine


class FixedSamplingTests(unittest.TestCase):
    def test_speaker_and_sampling_settings_stay_fixed_across_attempts(self) -> None:
        calls: list[dict] = []

        class FakeModel:
            def generate_custom_voice(self, **kwargs):
                calls.append(kwargs)
                return [SimpleNamespace(
                    sample_rate=24000,
                    audio=np.ones(24000, dtype=np.float32),
                    processing_time_seconds=0.0,
                    peak_memory_usage=0.0,
                )]

        engine = object.__new__(QwenTTSMLXEngine)
        engine.config = QwenTTSConfig(
            model="test-model",
            american_speaker="aiden",
            british_speaker="ryan",
            warmup=False,
            max_word_seconds=8,
            max_sentence_seconds=20,
            max_long_sentence_seconds=45,
            generation_timeout=0,
        )
        engine.model = FakeModel()
        engine.model_id = "test-model"
        engine.supported_speakers = ["aiden", "ryan"]
        engine.american_speaker = "aiden"
        engine.british_speaker = "ryan"

        engine.synthesize("hello", "word", "en-US", attempt=1, speaker="aiden")
        engine.synthesize(
            "hello", "word", "en-US", attempt=3, retry_variant=7, speaker="aiden"
        )

        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0]["speaker"], "aiden")
        sampling_keys = ("temperature", "top_k", "top_p", "repetition_penalty")
        self.assertEqual(
            tuple(calls[0][key] for key in sampling_keys),
            tuple(calls[1][key] for key in sampling_keys),
        )
        self.assertEqual(
            tuple(calls[0][key] for key in sampling_keys),
            (0.65, 40, 0.85, 1.10),
        )


if __name__ == "__main__":
    unittest.main()
