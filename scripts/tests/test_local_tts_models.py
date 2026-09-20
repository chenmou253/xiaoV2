import os
import unittest
from unittest import mock

from common import tts_providers
from common.qwen_tts_mlx import QwenTTSMLXEngine


class LocalTTSModelRegistryTests(unittest.TestCase):
    def test_local_model_repositories(self):
        self.assertEqual(
            tts_providers.LOCAL_QWEN_MODELS["local-qwen3-tts"],
            os.getenv(
                "TTS_MODEL_06B",
                os.getenv(
                    "TTS_MODEL",
                    "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit",
                ),
            ),
        )
        self.assertEqual(
            tts_providers.LOCAL_QWEN_MODELS["local-qwen3-tts-1.7b"],
            os.getenv(
                "TTS_MODEL_17B",
                "mlx-community/Qwen3-TTS-12Hz-1.7B-CustomVoice-8bit",
            ),
        )

    def test_both_local_models_use_same_engine_instructions(self):
        self.assertIsInstance(QwenTTSMLXEngine.instruct_for("sentence", "en-US"), str)
        self.assertIsInstance(QwenTTSMLXEngine.instruct_for("word", "en-US"), str)
        self.assertIsInstance(QwenTTSMLXEngine.instruct_for("sentence", "en-GB"), str)
        self.assertIsInstance(QwenTTSMLXEngine.instruct_for("word", "en-GB"), str)

    def test_provider_passes_selected_repo_to_mlx_engine(self):
        captured = {}

        class FakeEngine:
            supported_speakers = ["aiden", "ryan"]

            def __init__(self, config):
                captured["model"] = config.model

        with mock.patch.object(tts_providers, "QwenTTSMLXEngine", FakeEngine):
            provider = tts_providers.LocalQwenTTSProvider("local-qwen3-tts-1.7b")
        self.assertEqual(provider.model_id, "local-qwen3-tts-1.7b")
        self.assertEqual(
            captured["model"],
            tts_providers.LOCAL_QWEN_MODELS["local-qwen3-tts-1.7b"],
        )
        self.assertEqual(provider.supported_speakers, {"aiden", "ryan"})


if __name__ == "__main__":
    unittest.main()
