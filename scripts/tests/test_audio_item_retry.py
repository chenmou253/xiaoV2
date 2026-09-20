import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

import numpy as np

from generate_audio import AudioGenerator, _safe_component


class PassingGate:
    def check(self, *_args, **_kwargs):
        return {
            "passed": True,
            "attempt": 1,
            "reasons": [],
            "asr": {"exact": True},
            "pronunciation": {"passed": True},
            "audio": {"passed": True},
            "final_score": 1.0,
        }


class AudioItemRetryTest(unittest.TestCase):
    def test_retry_replaces_only_selected_manifest_entry_and_cleans_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            book = root / "grade-4-up"
            metadata = book / "metadata" / "pages"
            tts = book / "tts"
            metadata.mkdir(parents=True)
            tts.mkdir(parents=True)
            content = {
                "page": 3,
                "segments": [{
                    "id": "p3-s0",
                    "text": "Hello world.",
                    "words": [
                        {"id": "p3-s0-w0", "text": "Hello"},
                        {"id": "p3-s0-w1", "text": "world"},
                    ],
                }],
            }
            (metadata / "page-003.json").write_text(json.dumps(content))
            untouched = {
                "page": 3, "item_id": "p3-s0-w1", "accent": "en-US",
                "file": "page-003/words/untouched.wav",
            }
            selected_failure = {
                "page": 3, "item_id": "p3-s0-w0", "segment_id": "p3-s0",
                "type": "word", "text": "Hello", "context": "Hello world.",
                "accent": "en-US", "reasons": ["asr_text_mismatch"],
            }
            manifest = {"schema_version": 2, "items": [untouched], "failures": [selected_failure]}
            (tts / "manifest.json").write_text(json.dumps(manifest))
            failed_dir = tts / "audio_failed" / "page-003" / _safe_component("p3-s0-w0", "word") / "en-US"
            failed_dir.mkdir(parents=True)
            (failed_dir / "old.wav").write_bytes(b"old")

            generator = object.__new__(AudioGenerator)
            generator.engine = SimpleNamespace(
                model_id="test-qwen",
                speaker_for=lambda accent: "ryan" if accent == "en-US" else "aiden",
            )
            generator.config = SimpleNamespace(max_retry=1)
            generator.gate = PassingGate()
            generator._synthesize = lambda _item, _attempt, _variant: (
                np.zeros(2400, dtype=np.float32), 24000, [], "həlˈoʊ",
                {"method": "test"}, "hello", 0.9,
            )

            summary = generator.generate_page(root, "grade-4-up", 3, item_id="p3-s0-w0", accent="en-US")
            self.assertEqual(summary["total"], 1)
            self.assertEqual(summary["passed"], 1)
            updated = json.loads((tts / "manifest.json").read_text())
            self.assertIn(untouched, updated["items"])
            self.assertTrue(any(item["item_id"] == "p3-s0-w0" and item["accent"] == "en-US" for item in updated["items"]))
            self.assertFalse(any(item["item_id"] == "p3-s0-w0" and item["accent"] == "en-US" for item in updated["failures"]))
            self.assertFalse(failed_dir.exists())


if __name__ == "__main__":
    unittest.main()
