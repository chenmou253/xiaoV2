from __future__ import annotations

import unittest
from types import SimpleNamespace

from generate_audio import AudioGenerator, AudioItem


class PerBookAudioPlanningTests(unittest.TestCase):
    def setUp(self) -> None:
        self.generator = object.__new__(AudioGenerator)
        self.generator.engine = SimpleNamespace(
            american_speaker="aiden",
            british_speaker="ryan",
            speaker_for=lambda accent: "aiden" if accent == "en-US" else "ryan",
        )
        self.content = {"segments": [{
            "id": "p1-s1", "text": "Apple is good.",
            "words": [{"id": "p1-s1-w1", "text": "Apple"}],
        }]}

    def test_american_only_creates_only_en_us_with_locked_voice(self) -> None:
        items = self.generator._items(self.content, 1, {"en-US": "aiden"})
        self.assertEqual({item.accent for item in items}, {"en-US"})
        self.assertTrue(all(item.voice == "aiden" for item in items))

    def test_british_only_creates_only_en_gb_with_locked_voice(self) -> None:
        items = self.generator._items(self.content, 1, {"en-GB": "ryan"})
        self.assertEqual({item.accent for item in items}, {"en-GB"})
        self.assertTrue(all(item.voice == "ryan" for item in items))

    def test_both_accents_create_independent_items(self) -> None:
        items = self.generator._items(
            self.content, 1, {"en-US": "aiden", "en-GB": "ryan"})
        self.assertEqual(len(items), 4)
        self.assertEqual({item.accent for item in items}, {"en-US", "en-GB"})

    def test_mismatched_voice_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "en-US is locked.*aiden"):
            self.generator._items(self.content, 1, {"en-US": "ryan"})

    def test_no_enabled_accent_creates_no_items(self) -> None:
        self.assertEqual(self.generator._items(self.content, 1, {}), [])

    def test_unknown_accent_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "unsupported accent"):
            self.generator._items(self.content, 1, {"en-AU": "aiden"})

    def test_equal_words_in_different_contexts_keep_occurrence_ids(self) -> None:
        content = {"segments": [
            {"id": "s1", "text": "Apple is red.",
             "words": [{"id": "s1-w1", "text": "Apple"}]},
            {"id": "s2", "text": "Apple is sweet.",
             "words": [{"id": "s2-w1", "text": "Apple"}]},
        ]}
        words = [item for item in self.generator._items(
            content, 1, {"en-US": "aiden"}) if item.kind == "word"]
        self.assertEqual([item.item_id for item in words], ["s1-w1", "s2-w1"])
        self.assertNotEqual(words[0].context, words[1].context)

    def test_standalone_symbols_do_not_get_word_audio(self) -> None:
        content = {"segments": [{
            "id": "s1", "text": "Hello, world!",
            "words": [
                {"id": "punct-question", "text": "?"},
                {"id": "punct-comma", "text": ","},
                {"id": "hello", "text": "Hello,"},
                {"id": "number", "text": "1"},
            ],
        }]}
        items = self.generator._items(content, 1, {"en-US": "aiden"})
        self.assertEqual(
            [item.item_id for item in items if item.kind == "word"],
            ["hello", "number"],
        )
        sentence_items = [item for item in items if item.kind == "sentence"]
        self.assertEqual(len(sentence_items), 1)
        self.assertEqual(sentence_items[0].text, "Hello, world!")

    def test_word_only_segment_skips_duplicate_sentence_audio(self) -> None:
        content = {"segments": [{
            "id": "s1", "text": "Activity", "audio_mode": "word_only",
            "words": [{"id": "w1", "text": "Activity"}],
        }]}

        items = self.generator._items(content, 1, {"en-US": "aiden"})

        self.assertEqual([(item.kind, item.item_id) for item in items], [("word", "w1")])

    def test_audio_disabled_segment_creates_no_items(self) -> None:
        content = {"segments": [{
            "id": "s1", "text": "Activity", "audio_mode": "none",
            "words": [{"id": "w1", "text": "Activity"}],
        }]}

        self.assertEqual(self.generator._items(content, 1, {"en-US": "aiden"}), [])


    def test_page_reuse_key_deduplicates_same_content_same_voice(self) -> None:
        first = AudioItem("word", "Cool!", "Cool!", 1, "s1", "w1",
                          0, 0, "en-US", "aiden")
        second = AudioItem("word", "cool", "Different context.", 1, "s2", "w2",
                           1, 0, "en-US", "aiden")
        self.assertEqual(
            self.generator._page_reuse_key(first),
            self.generator._page_reuse_key(second),
        )

    def test_page_reuse_key_keeps_accent_and_kind_separate(self) -> None:
        word = AudioItem("word", "hello", "hello", 1, "s1", "w1",
                         0, 0, "en-US", "aiden")
        british = AudioItem("word", "hello", "hello", 1, "s1", "w2",
                            0, 1, "en-GB", "ryan")
        sentence = AudioItem("sentence", "hello", "hello", 1, "s2", "s2",
                             1, None, "en-US", "aiden")
        self.assertNotEqual(
            self.generator._page_reuse_key(word),
            self.generator._page_reuse_key(british),
        )
        self.assertNotEqual(
            self.generator._page_reuse_key(word),
            self.generator._page_reuse_key(sentence),
        )

    def test_word_cache_key_ignores_model_voice_accent_and_prompt(self) -> None:
        generator = object.__new__(AudioGenerator)
        generator.model_id = "local-qwen3-tts"
        generator.config = SimpleNamespace(max_retry=3)
        generator.gate = SimpleNamespace()
        generator.engine = SimpleNamespace(model_id="first-model")
        item = AudioItem("word", "Apple", "Apple is red.", 1, "s1", "w1",
                         0, 0, "en-US", "aiden")
        first = generator._word_cache_key(item)
        generator.model_id = "another-model"
        generator.engine.model_id = "another-model"
        changed = AudioItem("word", "Apple", "Different context.", 2, "s2", "w2",
                            0, 0, "en-GB", "ryan")

        self.assertEqual(first, generator._word_cache_key(changed))


if __name__ == "__main__":
    unittest.main()
