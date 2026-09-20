"""OCR provider adapters that preserve the canonical row/word schema."""
from __future__ import annotations

import base64
import struct
import string
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Protocol

from common.dashscope_client import DashScopeClient, DashScopeError
from common.ocr import PaddleOCREngine
from common.qwen35_word_boxes import enrich_qwen35_rows


# Qwen3.5-OCR combines this user prompt with the built-in
# ``advanced_recognition`` task.  The task remains responsible for returning
# ``ocr_result.words_info`` and its pixel coordinates; this prompt only limits
# the language/content scope of what may be recognized.
QWEN_OCR_PROMPT = """只识别图片中的英文文本、阿拉伯数字和英文标点。
忽略中文、日文、韩文及其他语言文字。
不要翻译、不要补写图片中不存在的内容。
按从上到下、从左到右返回英文文本行，并保持高精度识别所需的坐标格式。"""

_ASCII_OCR_CHARS = set(string.ascii_letters + string.digits + string.punctuation + " \t\r\n")


def _english_only(text: str) -> str:
    """Keep only ASCII English letters, Arabic digits and punctuation."""
    filtered = "".join(character for character in text if character in _ASCII_OCR_CHARS)
    return " ".join(filtered.split())


@dataclass
class OCRResult:
    rows: list[dict]
    method: str
    request_id: str = ""
    quality: dict[str, Any] = field(default_factory=dict)


class OCRProvider(Protocol):
    model_id: str
    cloud: bool

    def recognize(self, image: Path) -> OCRResult: ...


class LocalPaddleOCRProvider:
    model_id = "local-paddleocr"
    cloud = False

    def __init__(self, engine: PaddleOCREngine | None = None) -> None:
        self.engine = engine

    def recognize(self, image: Path) -> OCRResult:
        if self.engine is None:
            self.engine = PaddleOCREngine()
        return OCRResult(self.engine.rows(image), "paddleocr-ppocrv5")


def _png_size(path: Path) -> tuple[int, int]:
    header = path.read_bytes()[:24]
    if len(header) != 24 or header[:8] != b"\x89PNG\r\n\x1a\n":
        raise DashScopeError("OCR input is not a valid PNG")
    width, height = struct.unpack(">II", header[16:24])
    if width < 1 or height < 1:
        raise DashScopeError("OCR input image has invalid dimensions")
    return width, height


class QwenOCRProvider:
    model_id = "qwen3.5-ocr"
    cloud = True

    def __init__(self, client: DashScopeClient | None = None,
                 word_locator: LocalPaddleOCRProvider | None = None) -> None:
        self.client = client or DashScopeClient()
        # This helper is deliberately used only by Qwen3.5-OCR.  It does not
        # change the local provider's recognition path or its returned rows.
        self.word_locator = word_locator or LocalPaddleOCRProvider()

    def recognize(self, image: Path) -> OCRResult:
        width, height = _png_size(image)
        encoded = base64.b64encode(image.read_bytes()).decode("ascii")
        payload = {
            "model": self.model_id,
            "input": {"messages": [{"role": "user", "content": [{
                "text": QWEN_OCR_PROMPT,
            }, {
                "image": "data:image/png;base64," + encoded,
                "min_pixels": 3072,
                "max_pixels": 8388608,
                "enable_rotate": False,
            }]}]},
            "parameters": {"ocr_options": {"task": "advanced_recognition"}},
        }
        body, request_id = self.client.generate(payload)
        try:
            content = body["output"]["choices"][0]["message"]["content"]
            result = next(item["ocr_result"] for item in content if item.get("ocr_result"))
            words_info = result["words_info"]
        except (KeyError, IndexError, StopIteration, TypeError) as exc:
            raise DashScopeError(f"Qwen OCR response is missing words_info id={request_id or '-'}") from exc
        rows: list[dict] = []
        for entry in words_info:
            text = _english_only(str(entry.get("text") or "")).strip()
            location = entry.get("location")
            if not text or not isinstance(location, list) or len(location) != 8:
                continue
            xs = [float(location[index]) for index in range(0, 8, 2)]
            ys = [float(location[index]) for index in range(1, 8, 2)]
            left, top = max(0.0, min(xs) / width), max(0.0, min(ys) / height)
            right, bottom = min(1.0, max(xs) / width), min(1.0, max(ys) / height)
            if right <= left or bottom <= top:
                continue
            box = [round(value, 6) for value in (left, top, right - left, bottom - top)]
            word = {"text": text, "needs_review": False, "box": box}
            rows.append({"text": text, "needs_review": False, "box": box, "words": [word]})
        if not rows:
            raise DashScopeError(f"Qwen OCR returned no positioned text id={request_id or '-'}")
        rows.sort(key=lambda row: (row["box"][1], row["box"][0]))
        try:
            paddle_rows = self.word_locator.recognize(image).rows
            rows, quality = enrich_qwen35_rows(rows, paddle_rows)
            method = "qwen3.5-ocr+paddle-word-boxes"
        except Exception as exc:
            # Qwen's line OCR remains useful when the optional local locator
            # is unavailable.  Produce editable word boxes, mark them for
            # review, and preserve the cloud result rather than fail a page.
            rows, quality = enrich_qwen35_rows(rows, [])
            quality["word_box_locator_error"] = str(exc)
            method = "qwen3.5-ocr+estimated-word-boxes"
        return OCRResult(rows, method, request_id, quality)


def create_ocr_provider(model_id: str, local: LocalPaddleOCRProvider | None = None) -> OCRProvider:
    if model_id == LocalPaddleOCRProvider.model_id:
        return local or LocalPaddleOCRProvider()
    if model_id == QwenOCRProvider.model_id:
        return QwenOCRProvider(word_locator=local)
    raise ValueError(f"unsupported OCR model: {model_id}")
