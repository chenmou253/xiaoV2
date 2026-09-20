from __future__ import annotations

import unittest

from common.ocr import paddle_result_rows, text_layer_is_reliable
from prepare_book import candidate_segments


class OCRQualityTests(unittest.TestCase):
    def test_accepts_a_normal_embedded_english_text_layer(self) -> None:
        rows = [{
            "text": "Welcome to our English class. Please open your book and read this story together.",
            "words": [],
        }]
        self.assertTrue(text_layer_is_reliable(rows))

    def test_rejects_fragmented_or_too_short_text(self) -> None:
        rows = [{"text": "B R E F B H A H E P U T E R S U x W w Y", "words": []}]
        self.assertFalse(text_layer_is_reliable(rows))
        self.assertFalse(text_layer_is_reliable([{"text": "Hello", "words": []}]))


class PaddleResultTests(unittest.TestCase):
    def test_uses_paddle_word_boxes_and_marks_low_confidence(self) -> None:
        result = {"res": {
            "rec_texts": ["Hello world"],
            "rec_scores": [0.72],
            "rec_boxes": [[10, 20, 210, 50]],
            "text_word": [["Hello", "world"]],
            "text_word_boxes": [[
                [[10, 20], [85, 20], [85, 50], [10, 50]],
                [[100, 20], [210, 20], [210, 50], [100, 50]],
            ]],
        }}

        rows = paddle_result_rows(result, 400, 200, review_confidence=0.8)

        self.assertEqual(rows[0]["text"], "Hello world")
        self.assertEqual(rows[0]["box"], [0.025, 0.1, 0.5, 0.15])
        self.assertEqual([word["text"] for word in rows[0]["words"]], ["Hello", "world"])
        self.assertTrue(rows[0]["words"][0]["needs_review"])
        self.assertEqual(rows[0]["words"][0]["box"], [0.025, 0.1, 0.1875, 0.15])

    def test_falls_back_to_proportional_boxes_when_word_boxes_are_absent(self) -> None:
        result = {"res": {
            "rec_texts": ["Good morning"],
            "rec_scores": [0.95],
            "rec_boxes": [[0, 0, 120, 20]],
        }}

        rows = paddle_result_rows(result, 120, 100)

        self.assertEqual([word["text"] for word in rows[0]["words"]], ["Good", "morning"])
        self.assertFalse(rows[0]["words"][0]["needs_review"])
        self.assertAlmostEqual(sum(word["box"][2] for word in rows[0]["words"]), 11 / 12)


class SegmentTests(unittest.TestCase):
    def test_merges_wrapped_prose_but_preserves_fill_in_layout(self) -> None:
        rows = [
            {"text": "Please read", "box": [0.1, 0.1, 0.3, 0.03], "words": [
                {"text": "Please", "box": [0.1, 0.1, 0.12, 0.03]},
                {"text": "read", "box": [0.23, 0.1, 0.08, 0.03]},
            ]},
            {"text": "the story.", "box": [0.1, 0.14, 0.3, 0.03], "words": [
                {"text": "the", "box": [0.1, 0.14, 0.06, 0.03]},
                {"text": "story.", "box": [0.17, 0.14, 0.12, 0.03]},
            ]},
            {"text": "Name: _____", "box": [0.1, 0.2, 0.3, 0.03], "words": [
                {"text": "Name:", "box": [0.1, 0.2, 0.1, 0.03]},
                {"text": "_____,", "box": [0.22, 0.2, 0.12, 0.03]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual(segments[0]["text"], "Please read the story.")
        self.assertEqual(segments[1]["text"], "Name: _____,")
        self.assertEqual(segments[0]["words"][0]["box"], [0.1, 0.1, 0.12, 0.03])

    def test_splits_sentences_inside_one_visual_ocr_line(self) -> None:
        rows = [
            {"text": "Doctors are great! My", "box": [0.1, 0.1, 0.48, 0.03], "words": [
                {"text": "Doctors", "box": [0.1, 0.1, 0.12, 0.03]},
                {"text": "are", "box": [0.23, 0.1, 0.05, 0.03]},
                {"text": "great!", "box": [0.29, 0.1, 0.09, 0.03]},
                {"text": "My", "box": [0.4, 0.1, 0.04, 0.03]},
            ]},
            {"text": "father is a doctor too.", "box": [0.1, 0.14, 0.48, 0.03], "words": [
                {"text": "father", "box": [0.1, 0.14, 0.09, 0.03]},
                {"text": "is", "box": [0.2, 0.14, 0.03, 0.03]},
                {"text": "a", "box": [0.24, 0.14, 0.02, 0.03]},
                {"text": "doctor", "box": [0.27, 0.14, 0.09, 0.03]},
                {"text": "too.", "box": [0.37, 0.14, 0.06, 0.03]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual([segment["text"] for segment in segments], [
            "Doctors are great!", "My father is a doctor too.",
        ])
        self.assertEqual([word["text"] for word in segments[1]["words"]], [
            "My", "father", "is", "a", "doctor", "too.",
        ])

    def test_reliable_single_word_segment_uses_word_audio_only(self) -> None:
        segments = candidate_segments([{
            "text": "Activity",
            "box": [0.1, 0.1, 0.15, 0.04],
            "words": [{
                "text": "Activity", "box": [0.1, 0.1, 0.15, 0.04],
                "confidence": 0.99, "needs_review": False,
            }],
        }], 1)

        self.assertEqual(segments[0]["audio_mode"], "word_only")

    def test_uncertain_single_token_keeps_legacy_sentence_audio(self) -> None:
        segments = candidate_segments([{
            "text": "Activity",
            "box": [0.1, 0.1, 0.15, 0.04],
            "words": [{
                "text": "Activity", "box": [0.1, 0.1, 0.15, 0.04],
                "confidence": 0.55, "needs_review": True,
            }],
        }], 1)

        self.assertNotIn("audio_mode", segments[0])

    def test_keeps_large_section_label_out_of_dialogue_body(self) -> None:
        rows = [
            {"text": "Let's talk", "box": [0.42, 0.06, 0.2, 0.06], "words": [
                {"text": "Let's", "box": [0.42, 0.06, 0.1, 0.06]},
                {"text": "talk", "box": [0.53, 0.06, 0.09, 0.06]},
            ]},
            {"text": "You have a big", "box": [0.3, 0.18, 0.32, 0.03], "words": [
                {"text": "You", "box": [0.3, 0.18, 0.06, 0.03]},
                {"text": "have", "box": [0.37, 0.18, 0.07, 0.03]},
                {"text": "a", "box": [0.45, 0.18, 0.02, 0.03]},
                {"text": "big", "box": [0.48, 0.18, 0.05, 0.03]},
            ]},
            {"text": "family.", "box": [0.3, 0.22, 0.12, 0.03], "words": [
                {"text": "family.", "box": [0.3, 0.22, 0.12, 0.03]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual([segment["text"] for segment in segments], [
            "Let's talk", "You have a big family.",
        ])

    def test_does_not_split_common_abbreviation(self) -> None:
        rows = [
            {"text": "Dr. Brown is here.", "box": [0.1, 0.1, 0.3, 0.03], "words": [
                {"text": "Dr.", "box": [0.1, 0.1, 0.04, 0.03]},
                {"text": "Brown", "box": [0.15, 0.1, 0.08, 0.03]},
                {"text": "is", "box": [0.24, 0.1, 0.03, 0.03]},
                {"text": "here.", "box": [0.28, 0.1, 0.06, 0.03]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual([segment["text"] for segment in segments], ["Dr. Brown is here."])

    def test_rejoins_wrapped_sentence_around_another_visual_region(self) -> None:
        rows = [
            {"text": "Doctors are great! My", "box": [0.1, 0.1, 0.28, 0.03], "words": [
                {"text": "Doctors", "box": [0.1, 0.1, 0.09, 0.03]},
                {"text": "are", "box": [0.2, 0.1, 0.04, 0.03]},
                {"text": "great!", "box": [0.25, 0.1, 0.07, 0.03]},
                {"text": "My", "box": [0.33, 0.1, 0.03, 0.03]},
            ]},
            {"text": "doctor.", "box": [0.74, 0.115, 0.1, 0.03], "words": [
                {"text": "doctor.", "box": [0.74, 0.115, 0.1, 0.03]},
            ]},
            {"text": "father is a doctor too.", "box": [0.1, 0.145, 0.28, 0.03], "words": [
                {"text": "father", "box": [0.1, 0.145, 0.07, 0.03]},
                {"text": "is", "box": [0.18, 0.145, 0.02, 0.03]},
                {"text": "a", "box": [0.21, 0.145, 0.01, 0.03]},
                {"text": "doctor", "box": [0.23, 0.145, 0.07, 0.03]},
                {"text": "too.", "box": [0.31, 0.145, 0.05, 0.03]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual([segment["text"] for segment in segments], [
            "Doctors are great!", "My father is a doctor too.", "doctor.",
        ])

    def test_rejoins_ordinary_wrapped_lines_when_columns_are_interleaved(self) -> None:
        rows = [
            {"text": "She's a", "box": [0.75, 0.54, 0.10, 0.022], "words": [
                {"text": "She's", "box": [0.75, 0.54, 0.06, 0.022]},
                {"text": "a", "box": [0.82, 0.54, 0.02, 0.022]},
            ]},
            {"text": "Doctors are great! My", "box": [0.10, 0.556, 0.28, 0.022], "words": [
                {"text": "Doctors", "box": [0.10, 0.556, 0.08, 0.022]},
                {"text": "are", "box": [0.19, 0.556, 0.04, 0.022]},
                {"text": "great!", "box": [0.24, 0.556, 0.07, 0.022]},
                {"text": "My", "box": [0.34, 0.556, 0.03, 0.022]},
            ]},
            {"text": "doctor.", "box": [0.755, 0.57, 0.09, 0.02], "words": [
                {"text": "doctor.", "box": [0.755, 0.57, 0.09, 0.02]},
            ]},
            {"text": "father is a doctor too.", "box": [0.10, 0.582, 0.28, 0.02], "words": [
                {"text": "father", "box": [0.10, 0.582, 0.07, 0.02]},
                {"text": "is", "box": [0.18, 0.582, 0.02, 0.02]},
                {"text": "a", "box": [0.21, 0.582, 0.01, 0.02]},
                {"text": "doctor", "box": [0.23, 0.582, 0.07, 0.02]},
                {"text": "too.", "box": [0.31, 0.582, 0.05, 0.02]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual([segment["text"] for segment in segments], [
            "She's a doctor.", "Doctors are great!", "My father is a doctor too.",
        ])

    def test_rejoins_wrapped_question_across_a_nearby_dialogue(self) -> None:
        rows = [
            {"text": "What's your", "box": [0.29, 0.83, 0.16, 0.022], "words": [
                {"text": "What's", "box": [0.29, 0.83, 0.08, 0.022]},
                {"text": "your", "box": [0.38, 0.83, 0.06, 0.022]},
            ]},
            {"text": "She's a teacher.", "box": [0.51, 0.842, 0.20, 0.02], "words": [
                {"text": "She's", "box": [0.51, 0.842, 0.06, 0.02]},
                {"text": "a", "box": [0.58, 0.842, 0.02, 0.02]},
                {"text": "teacher.", "box": [0.61, 0.842, 0.09, 0.02]},
            ]},
            {"text": "mother's job?", "box": [0.28, 0.863, 0.18, 0.022], "words": [
                {"text": "mother's", "box": [0.28, 0.863, 0.10, 0.022]},
                {"text": "job?", "box": [0.39, 0.863, 0.06, 0.022]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual([segment["text"] for segment in segments], [
            "What's your mother's job?", "She's a teacher.",
        ])

    def test_rejoins_detached_punctuation_and_contraction_tokens(self) -> None:
        rows = [
            {"text": "Let ' s talk", "box": [0.1, 0.1, 0.2, 0.05], "words": [
                {"text": "Let", "box": [0.1, 0.1, 0.06, 0.05]},
                {"text": "'", "box": [0.17, 0.1, 0.01, 0.05]},
                {"text": "s", "box": [0.19, 0.1, 0.02, 0.05]},
                {"text": "talk", "box": [0.22, 0.1, 0.06, 0.05]},
            ]},
            {"text": "family .", "box": [0.1, 0.18, 0.15, 0.03], "words": [
                {"text": "family", "box": [0.1, 0.18, 0.1, 0.03]},
                {"text": ".", "box": [0.21, 0.18, 0.01, 0.03]},
            ]},
        ]
        segments = candidate_segments(rows, 1)
        self.assertEqual([segment["text"] for segment in segments], ["Let's talk", "family."])
        self.assertEqual([word["text"] for word in segments[0]["words"]], ["Let's", "talk"])
        self.assertEqual([word["text"] for word in segments[1]["words"]], ["family."])
        self.assertEqual(segments[0]["words"][0]["box"], [0.1, 0.1, 0.11, 0.05])
        self.assertEqual(segments[1]["words"][0]["box"], [0.1, 0.18, 0.12, 0.03])

    def test_attaches_detached_sentence_punctuation_to_word_boxes(self) -> None:
        rows = [{
            "text": "Hello , world !",
            "box": [0.1, 0.1, 0.4, 0.04],
            "words": [
                {"text": "Hello", "box": [0.1, 0.1, 0.12, 0.04]},
                {"text": ",", "box": [0.225, 0.1, 0.01, 0.04]},
                {"text": "world", "box": [0.25, 0.1, 0.12, 0.04]},
                {"text": "!", "box": [0.375, 0.1, 0.01, 0.04]},
            ],
        }]

        segment = candidate_segments(rows, 1)[0]

        self.assertEqual(segment["text"], "Hello, world!")
        self.assertEqual([word["text"] for word in segment["words"]], ["Hello,", "world!"])
        self.assertEqual(segment["words"][0]["box"], [0.1, 0.1, 0.135, 0.04])
        self.assertEqual(segment["words"][1]["box"], [0.25, 0.1, 0.135, 0.04])

    def test_quotes_remain_in_sentence_without_standalone_boxes(self) -> None:
        rows = [{
            "text": 'He said " hello ".',
            "words": [
                {"text": "He", "box": [0.1, 0.1, 0.03, 0.04]},
                {"text": "said", "box": [0.14, 0.1, 0.06, 0.04]},
                {"text": '"', "box": [0.205, 0.1, 0.005, 0.04]},
                {"text": "hello", "box": [0.215, 0.1, 0.07, 0.04]},
                {"text": '"', "box": [0.29, 0.1, 0.005, 0.04]},
                {"text": ".", "box": [0.297, 0.1, 0.005, 0.04]},
            ],
        }]

        segment = candidate_segments(rows, 1)[0]

        self.assertEqual(segment["text"], 'He said "hello".')
        self.assertEqual([word["text"] for word in segment["words"]], [
            "He", "said", '"hello".',
        ])


if __name__ == "__main__":
    unittest.main()
