from __future__ import annotations

import base64
import io
import struct
import tempfile
import unittest
import wave
from pathlib import Path

from common.ocr_providers import QWEN_OCR_PROMPT, QwenOCRProvider, _english_only
from common.ocr_providers import OCRResult
from common.tts_providers import QwenFlashTTSProvider


class FakeClient:
    def __init__(self, body: dict):
        self.body = body
        self.calls = 0
        self.payload = None

    def generate(self, payload: dict):
        self.calls += 1
        self.payload = payload
        return self.body, "request-test-1"

    def download(self, _url: str, _target: Path):
        raise AssertionError("test response uses inline audio")


class FakeWordLocator:
    def __init__(self, rows: list[dict]):
        self.rows = rows
        self.calls = 0

    def recognize(self, _image: Path) -> OCRResult:
        self.calls += 1
        return OCRResult(self.rows, "fake-paddle")


class FailingWordLocator:
    def recognize(self, _image: Path) -> OCRResult:
        raise RuntimeError("Paddle word locator unavailable")


def wav_data() -> bytes:
    output = io.BytesIO()
    with wave.open(output, "wb") as audio:
        audio.setnchannels(1)
        audio.setsampwidth(2)
        audio.setframerate(24000)
        audio.writeframes(b"\0\0" * 2400)
    return output.getvalue()


class CloudProviderTests(unittest.TestCase):
    def test_qwen_ocr_filters_non_english_characters(self):
        self.assertEqual(_english_only("中文 Hello，１２３"), "Hello")

    def test_qwen_ocr_makes_one_call_and_preserves_positioned_schema(self):
        client = FakeClient({
            "output": {"choices": [{"message": {"content": [{"ocr_result": {
                "words_info": [{"text": "Hello", "location": [10, 20, 110, 20, 110, 60, 10, 60]}]
            }}]}}]},
        })
        with tempfile.TemporaryDirectory() as directory:
            image = Path(directory) / "page.png"
            image.write_bytes(b"\x89PNG\r\n\x1a\n" + b"\0" * 8 + struct.pack(">II", 200, 100))
            result = QwenOCRProvider(client, word_locator=FakeWordLocator([{
                "words": [{"text": "Hello", "box": [0.05, 0.2, 0.5, 0.4], "confidence": 1.0}]
            }])).recognize(image)
        self.assertEqual(client.calls, 1)
        self.assertEqual(result.request_id, "request-test-1")
        self.assertEqual(result.rows[0]["words"][0]["box"], [0.05, 0.2, 0.5, 0.4])
        self.assertEqual(
            client.payload["input"]["messages"][0]["content"][0]["text"],
            QWEN_OCR_PROMPT,
        )

    def test_qwen_ocr_uses_paddle_only_for_word_boxes(self):
        sentence = '4. I can sound out words with "ch" to spell them.'
        tokens = sentence.split()
        word_rows = [{"words": [
            {"text": token, "box": [index / len(tokens), 0.1, 1 / len(tokens), 0.2], "confidence": 0.98}
            for index, token in enumerate(tokens)
        ]}]
        locator = FakeWordLocator(word_rows)
        client = FakeClient({
            "output": {"choices": [{"message": {"content": [{"ocr_result": {
                "words_info": [{"text": sentence, "location": [0, 10, 200, 10, 200, 30, 0, 30]}]
            }}]}}]},
        })
        with tempfile.TemporaryDirectory() as directory:
            image = Path(directory) / "page.png"
            image.write_bytes(b"\x89PNG\r\n\x1a\n" + b"\0" * 8 + struct.pack(">II", 200, 100))
            result = QwenOCRProvider(client, word_locator=locator).recognize(image)
        words = result.rows[0]["words"]
        self.assertEqual(locator.calls, 1)
        self.assertEqual([word["text"] for word in words], tokens)
        self.assertEqual(words[3]["text"], "sound")
        self.assertNotEqual(words[0]["box"], words[-1]["box"])
        self.assertEqual(result.method, "qwen3.5-ocr+paddle-word-boxes")
        self.assertEqual(result.quality["word_box_match_rate"], 1.0)

    def test_qwen_ocr_estimates_word_boxes_when_locator_fails(self):
        client = FakeClient({
            "output": {"choices": [{"message": {"content": [{"ocr_result": {
                "words_info": [{"text": "Read and write.", "location": [0, 10, 180, 10, 180, 30, 0, 30]}]
            }}]}}]},
        })
        with tempfile.TemporaryDirectory() as directory:
            image = Path(directory) / "page.png"
            image.write_bytes(b"\x89PNG\r\n\x1a\n" + b"\0" * 8 + struct.pack(">II", 180, 100))
            result = QwenOCRProvider(client, word_locator=FailingWordLocator()).recognize(image)
        words = result.rows[0]["words"]
        self.assertEqual([word["text"] for word in words], ["Read", "and", "write."])
        self.assertTrue(all(word["needs_review"] for word in words))
        self.assertEqual(result.method, "qwen3.5-ocr+estimated-word-boxes")
        self.assertEqual(result.quality["estimated_word_count"], 3)

    def test_qwen_text_is_not_replaced_by_paddle_text(self):
        client = FakeClient({
            "output": {"choices": [{"message": {"content": [{"ocr_result": {
                "words_info": [{"text": "Read and write.", "location": [0, 10, 180, 10, 180, 30, 0, 30]}]
            }}]}}]},
        })
        locator = FakeWordLocator([{"words": [
            {"text": "Read", "box": [0.0, 0.1, 0.3, 0.2], "confidence": 0.99},
            {"text": "and", "box": [0.35, 0.1, 0.2, 0.2], "confidence": 0.99},
            {"text": "wrlte.", "box": [0.6, 0.1, 0.4, 0.2], "confidence": 0.60},
        ]}])
        with tempfile.TemporaryDirectory() as directory:
            image = Path(directory) / "page.png"
            image.write_bytes(b"\x89PNG\r\n\x1a\n" + b"\0" * 8 + struct.pack(">II", 180, 100))
            result = QwenOCRProvider(client, word_locator=locator).recognize(image)
        word = result.rows[0]["words"][-1]
        self.assertEqual(word["text"], "write.")
        self.assertTrue(word["needs_review"])

    def test_qwen_tts_makes_one_call_and_forbids_automatic_retry(self):
        client = FakeClient({
            "output": {"audio": {"data": base64.b64encode(wav_data()).decode("ascii")}},
        })
        provider = QwenFlashTTSProvider(client)
        samples, sample_rate, generation = provider.synthesize(
            "Hello", "word", "en-US", 1, 0, "Aiden"
        )
        self.assertEqual(client.calls, 1)
        self.assertEqual(sample_rate, 24000)
        self.assertGreater(len(samples), 0)
        self.assertEqual(generation["request_id"], "request-test-1")
        with self.assertRaisesRegex(RuntimeError, "automatic retry is disabled"):
            provider.synthesize("Hello", "word", "en-US", 2, 0, "Aiden")
        self.assertEqual(client.calls, 1)


if __name__ == "__main__":
    unittest.main()
