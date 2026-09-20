from __future__ import annotations

import json
import tempfile
import types
import unittest
from pathlib import Path
from types import SimpleNamespace

import numpy as np
import soundfile as sf

from common.audio_pronunciation import check_pronunciation
from common.audio_qa import AudioQAConfig, AudioQualityGate
from common.audio_quality import analyze_audio, detect_repetition
from generate_audio import AudioGenerator, PageAudioError


RATE = 24000


def speech_like(seconds: float = 1.2) -> np.ndarray:
    t = np.arange(int(RATE * seconds), dtype=np.float32) / RATE
    sweep = np.sin(2 * np.pi * (150 * t + 160 * t * t))
    harmonic = 0.35 * np.sin(2 * np.pi * (310 * t + 90 * t * t))
    envelope = 0.55 + 0.45 * np.sin(2 * np.pi * 3.7 * t) ** 2
    return (0.16 * (sweep + harmonic) * envelope).astype(np.float32)


class WaveformQualityTests(unittest.TestCase):
    def test_normal_audio_passes(self) -> None:
        result = analyze_audio(speech_like(), RATE, "sentence", "The little boy is playing.")
        self.assertTrue(result["passed"], result)

    def test_detects_silence_clipping_and_short_audio(self) -> None:
        silent = analyze_audio(np.zeros(RATE, dtype=np.float32), RATE, "word", "cat")
        self.assertIn("near_silent_audio", silent["reasons"])
        clipped = analyze_audio(np.ones(RATE, dtype=np.float32), RATE, "word", "cat")
        self.assertIn("clipping_detected", clipped["reasons"])
        short = analyze_audio(speech_like(0.05), RATE, "word", "apple")
        self.assertIn("audio_too_short", short["reasons"])

    def test_detects_long_silence_and_repeated_tail(self) -> None:
        value = np.concatenate([speech_like(0.4), np.zeros(int(RATE * 0.9)), speech_like(0.4)])
        result = analyze_audio(value, RATE, "sentence", "hello world")
        self.assertIn("continuous_silence_too_long", result["reasons"])
        rng = np.random.default_rng(7)
        prefix = rng.normal(0, 0.05, RATE).astype(np.float32)
        repeated = rng.normal(0, 0.08, RATE // 2).astype(np.float32)
        loop = np.concatenate([prefix, repeated, repeated])
        self.assertTrue(detect_repetition(loop, RATE)["detected"])

    def test_detects_local_loop_that_is_not_at_the_tail(self) -> None:
        rng = np.random.default_rng(11)
        prefix = rng.normal(0, 0.03, RATE // 4).astype(np.float32)
        repeated = rng.normal(0, 0.08, RATE // 2).astype(np.float32)
        suffix = rng.normal(0, 0.04, RATE // 2).astype(np.float32)
        result = detect_repetition(np.concatenate([prefix, repeated, repeated, suffix]), RATE)
        self.assertTrue(result["detected"], result)
        self.assertEqual(result["location"], "local")

    def test_detects_edge_silence_long_audio_and_sample_rate(self) -> None:
        value = np.concatenate([np.zeros(int(RATE * 0.5)), speech_like(0.4),
                                np.zeros(int(RATE * 0.7))])
        result = analyze_audio(value, RATE, "word", "apple")
        self.assertIn("leading_silence_too_long", result["reasons"])
        self.assertIn("trailing_silence_too_long", result["reasons"])
        long_result = analyze_audio(speech_like(4.5), RATE, "word", "cat")
        self.assertIn("audio_too_long", long_result["reasons"])
        wrong_rate = analyze_audio(speech_like(), 16000, "sentence", "hello")
        self.assertIn("unexpected_sample_rate", wrong_rate["reasons"])

    def test_corrupt_wav_fails_before_asr(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "broken.wav"
            path.write_bytes(b"not a wav")
            gate = AudioQualityGate(AudioQAConfig(enabled=False))
            result = gate.check(path, speech_like(), RATE, "word", "cat", "kæt", [], 1)
            self.assertEqual(result["reasons"], ["damaged_wav"])


class WordAlignmentTests(unittest.TestCase):
    def test_qwen_path_uses_asr_words_when_phoneme_timings_are_unavailable(self) -> None:
        result = check_pronunciation(
            speech_like(), RATE, "hello world", "", [],
            {"words": [
                {"word": "hello", "start": 0.05, "end": 0.50, "probability": 0.98},
                {"word": "world", "start": 0.52, "end": 1.05, "probability": 0.97},
            ]},
        )
        self.assertEqual(result["method"], "whisper-word-alignment+waveform-energy")
        self.assertGreaterEqual(result["score"], 0.9)


class GenerationIsolationTests(unittest.TestCase):
    class Gate:
        def check(self, _path, _samples, _rate, _kind, expected, _phonemes, _timings, attempt):
            passed = attempt > 1
            return {
                "passed": passed, "attempt": attempt, "asr": {"expected": expected,
                "recognized": expected, "wer": 0.0, "cer": 0.0, "passed": passed},
                "pronunciation": {"score": 0.95, "passed": passed},
                "audio": {"duration": 0.5, "passed": passed,
                          "repetition_detected": False, "reasons": []},
                "final_score": 0.95 if passed else 0.2,
                "reasons": [] if passed else ["forced_test_failure"],
            }

    def test_duplicate_words_share_qa_passed_content_addressed_audio(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            metadata = root / "book" / "metadata" / "pages"
            metadata.mkdir(parents=True)
            metadata.joinpath("page-001.json").write_text(json.dumps({"segments": [{
                "id": "s1", "text": "very very good", "words": [
                    {"id": "w1", "text": "very"}, {"id": "w2", "text": "very"},
                ]
            }]}))
            generator = object.__new__(AudioGenerator)
            generator.engine = SimpleNamespace(
                model_id="test-qwen",
                speaker_for=lambda accent: "ryan" if accent == "en-US" else "aiden",
            )
            generator.config = SimpleNamespace(max_retry=3)
            generator.gate = self.Gate()
            samples = speech_like(0.5)
            generator._synthesize = types.MethodType(
                lambda _self, item, _attempt, _variant: (
                    samples, RATE, [], "fəʊn", {"method": "test", "context": item.context},
                    item.text, 0.9,
                ), generator,
            )
            first = generator.generate_page(root, "book", 1)
            first_manifest = json.loads((root / "book" / "tts" / "manifest.json").read_text())
            first_files = {entry["file"] for entry in first_manifest["items"]}
            first_word_entries = [entry for entry in first_manifest["items"]
                                  if entry["kind"] == "word"]
            missing = generator.generate_page(root, "book", 1, mode="missing")
            missing_manifest = json.loads((root / "book" / "tts" / "manifest.json").read_text())
            self.assertEqual(missing["total"], 0)
            self.assertEqual({entry["file"] for entry in missing_manifest["items"]}, first_files)
            second = generator.generate_page(root, "book", 1)
            second_manifest = json.loads((root / "book" / "tts" / "manifest.json").read_text())
            second_files = {entry["file"] for entry in second_manifest["items"]}
            self.assertEqual(first["total"], 6)
            self.assertEqual(first["retried"], 3)
            self.assertEqual(first["reused"], 3)
            self.assertEqual(second["passed"], 6)
            self.assertEqual(second["reused"], 4)
            self.assertEqual(len(first_manifest["items"]), 6)
            self.assertEqual(len(first_files), 3)
            self.assertEqual(len(second_files), 3)
            self.assertEqual(len({entry["file"] for entry in first_word_entries}), 1)
            self.assertEqual(
                len({entry["word_cache_key"] for entry in first_word_entries}), 1,
            )
            self.assertEqual(len(first_files & second_files), 1)

    def test_context_sensitive_word_uses_the_same_draft_word_key(self) -> None:
        generator = object.__new__(AudioGenerator)
        generator.engine = SimpleNamespace(model_id="test-qwen")
        generator.config = SimpleNamespace(max_retry=3)
        items = generator._items({"segments": [{
            "id": "s1", "text": "I read and read.", "words": [
                {"id": "w1", "text": "read"}, {"id": "w2", "text": "read"},
            ],
        }]}, 1, {"en-US": "aiden"})

        keys = [generator._word_cache_key(item) for item in items if item.kind == "word"]
        self.assertTrue(keys[0])
        self.assertEqual(keys[0], keys[1])

    def test_existing_draft_word_audio_is_promoted_and_reused(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            book = root / "book"
            metadata = book / "metadata" / "pages"
            metadata.mkdir(parents=True)
            metadata.joinpath("page-002.json").write_text(json.dumps({
                "segments": [{
                    "id": "s2", "text": "Apple", "audio_mode": "word_only",
                    "words": [{"id": "p2-apple", "text": "Apple"}],
                }],
            }))
            legacy = book / "tts" / "page-001" / "words" / "apple.wav"
            legacy.parent.mkdir(parents=True)
            sf.write(legacy, speech_like(0.5), RATE, subtype="PCM_16")
            (book / "tts" / "manifest.json").write_text(json.dumps({
                "schema_version": 2, "model": "old-model",
                "items": [{
                    "page": 1, "item_id": "p1-apple", "segment_id": "s1",
                    "kind": "word", "text": "Apple", "accent": "en-GB",
                    "voice": "old-voice", "file": "page-001/words/apple.wav",
                    "qa": {"passed": True},
                }],
                "failures": [],
            }))
            generator = object.__new__(AudioGenerator)
            generator.engine = SimpleNamespace(model_id="new-model")
            generator.config = SimpleNamespace(max_retry=3)
            generator.gate = self.Gate()
            generator._synthesize = types.MethodType(
                lambda *_args: (_ for _ in ()).throw(
                    AssertionError("reused word must not call TTS")
                ),
                generator,
            )

            summary = generator.generate_page(
                root, "book", 2, voices={"en-US": "aiden"},
            )
            manifest = json.loads((book / "tts" / "manifest.json").read_text())
            word_files = {entry["file"] for entry in manifest["items"]}

            self.assertEqual(summary["reused"], 1)
            self.assertEqual(len(word_files), 1)
            self.assertTrue(next(iter(word_files)).startswith("word-cache/"))
            self.assertTrue((book / "tts" / next(iter(word_files))).is_file())

    def test_failed_regeneration_keeps_previous_formal_mapping(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            metadata = root / "book" / "metadata" / "pages"
            metadata.mkdir(parents=True)
            metadata.joinpath("page-001.json").write_text(json.dumps({"segments": [{
                "id": "s1", "text": "hello", "words": [],
            }]}))
            generator = object.__new__(AudioGenerator)
            generator.engine = SimpleNamespace(
                model_id="test-qwen",
                speaker_for=lambda accent: "ryan" if accent == "en-US" else "aiden",
            )
            generator.config = SimpleNamespace(max_retry=1)
            samples = speech_like(0.5)
            generator._synthesize = types.MethodType(
                lambda _self, item, _attempt, _variant: (
                    samples, RATE, [], "həlˈoʊ", {"method": "test", "context": item.context},
                    item.text, 0.9,
                ), generator,
            )
            generator.gate = self.Gate()
            generator.config = SimpleNamespace(max_retry=2)
            generator.generate_page(root, "book", 1)
            self.assertEqual(len(json.loads(
                (root / "book" / "tts" / "manifest.json").read_text()
            )["items"]), 2)

            class AlwaysFail:
                def check(self, _path, _samples, _rate, _kind, expected,
                          _phonemes, _timings, attempt):
                    return {
                        "passed": False, "attempt": attempt,
                        "asr": {"expected": expected, "recognized": "wrong",
                                "wer": 1.0, "cer": 1.0, "passed": False},
                        "pronunciation": {"score": 0.1, "passed": False},
                        "audio": {"passed": True, "reasons": []},
                        "final_score": 0.1, "reasons": ["forced_test_failure"],
                    }

            generator.gate = AlwaysFail()
            generator.config = SimpleNamespace(max_retry=1)
            with self.assertRaises(PageAudioError):
                generator.generate_page(root, "book", 1)
            manifest = json.loads((root / "book" / "tts" / "manifest.json").read_text())
            self.assertEqual(len(manifest["items"]), 2)
            self.assertEqual(len(manifest["failures"]), 2)


if __name__ == "__main__":
    unittest.main()
