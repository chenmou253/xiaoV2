#!/usr/bin/env python3
"""Resident local translation daemon for xiaoV2.

Protocol: one program-generated JSON request per stdin line.

Sentence request:
  {"task":"sentence","text":"...","model_id":"local-qwen3-4b-instruct-2507"}

Word request:
  {"task":"word","text":"...","sentence":"...","model_id":"local-qwen3-4b-instruct-2507"}

The Qwen model itself never generates JSON. It returns either one translation
line (sentence) or exactly two lines (word meaning + General American IPA).
The daemon validates that plain text and wraps the validated result in stable
JSON for the Go service.
"""
from __future__ import annotations

import json
import re
import sys

from translate_page import LocalMLXBackend, clean_phonetic, clean_translation, normalize_source_text

LOCAL_MODEL_ID = "local-qwen3-4b-instruct-2507"

SENTENCE_SYSTEM_PROMPT = """You are a deterministic English-to-Chinese translator for Chinese primary-school textbooks.
Translate only the supplied English sentence into concise, natural Simplified Chinese suitable for children.
Preserve meaning, negation, names, numbers, dates, times, and factual information.
Return exactly one line containing only the Chinese translation.
Do not output labels, quotes, Markdown, explanations, notes, reasoning, or JSON."""

WORD_SYSTEM_PROMPT = """You are a deterministic vocabulary editor for Chinese primary-school English textbooks.
Translate and pronounce ONLY the supplied English word. Ignore all sentence context.

Return exactly two lines:
Line 1: ONE short common Simplified Chinese dictionary gloss for the word.
Line 2: ONE General American English IPA pronunciation for the word.

Critical rules:
- Never translate a sentence or phrase.
- Never output an example, explanation, part-of-speech label, or multiple meanings.
- Line 1 should normally be 1-6 Chinese characters.
- Line 2 must be IPA only.
- Use IPA stress marks ˈ or ˌ when needed.
- Use IPA length mark ː when needed; never use ASCII colon ":" for vowel length.
- Never mix ordinary English spelling into the IPA.
- Never use apostrophe "'" as a stress mark.
- No labels, quotes, Markdown, explanations, notes, reasoning, or JSON.

Examples:
for
给
fɔːr

listen
听
ˈlɪsən

hospital
医院
ˈhɑːspɪtl
"""

THINK_RE = re.compile(r"<think>.*?</think>", re.IGNORECASE | re.DOTALL)
MEANING_LABEL_RE = re.compile(r"^(?:meaning|chinese meaning|词义|中文词义)\s*[:：]\s*", re.IGNORECASE)
PHONETIC_LABEL_RE = re.compile(r"^(?:phonetic|ipa|音标)\s*[:：]\s*", re.IGNORECASE)
CODE_FENCE_LINE_RE = re.compile(r"^```(?:text|markdown)?\s*$|^```\s*$", re.IGNORECASE)


class LocalItemValidationError(ValueError):
    """The local model answered, but its plain-text result is not safely usable."""


def _remove_thinking(value: str) -> str:
    cleaned = THINK_RE.sub("", value).strip()
    if "</think>" in cleaned.lower():
        cleaned = re.split(r"</think>", cleaned, flags=re.IGNORECASE)[-1].strip()
    return cleaned


def _content_lines(value: str) -> list[str]:
    text = _remove_thinking(str(value or ""))
    lines: list[str] = []
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or CODE_FENCE_LINE_RE.fullmatch(line):
            continue
        line = re.sub(r"^>\s?", "", line).strip()
        if line:
            lines.append(line)
    return lines


def parse_sentence_result(value: str) -> str:
    lines = _content_lines(value)
    if len(lines) != 1:
        raise LocalItemValidationError(
            f"sentence result must contain exactly one non-empty line, got {len(lines)}"
        )
    translation = clean_translation(lines[0]).strip().strip('"“”')
    if not translation:
        raise LocalItemValidationError("sentence translation is empty")
    if len(translation) > 1000:
        raise LocalItemValidationError("sentence translation is unexpectedly long")
    return translation


def parse_word_result(value: str, source_word: str) -> tuple[str, str]:
    lines = _content_lines(value)
    if len(lines) != 2:
        raise LocalItemValidationError(
            f"word result must contain exactly two non-empty lines, got {len(lines)}"
        )
    meaning = MEANING_LABEL_RE.sub("", lines[0], count=1).strip().strip('"“”')
    meaning = clean_translation(meaning)
    phonetic_raw = PHONETIC_LABEL_RE.sub("", lines[1], count=1).strip()
    phonetic = clean_phonetic(phonetic_raw)
    if not meaning:
        raise LocalItemValidationError("word meaning is empty")
    source_tokens = [token for token in normalize_source_text(source_word).split(" ") if token]
    max_meaning_chars = 6 if len(source_tokens) <= 1 else 12
    if len(meaning) > max_meaning_chars:
        raise LocalItemValidationError(
            f"word meaning is too long for a dictionary gloss: {len(meaning)} > {max_meaning_chars}"
        )
    if re.search(r"[，。！？；：,.!?;:]", meaning):
        raise LocalItemValidationError("word meaning looks like a sentence or multiple meanings")
    if not phonetic:
        raise LocalItemValidationError("word phonetic is empty or invalid")
    source = normalize_source_text(source_word).casefold()
    lowered_phonetic = phonetic.casefold()
    if lowered_phonetic == source:
        raise LocalItemValidationError("word phonetic repeats the source word instead of IPA")
    if source and source in lowered_phonetic:
        raise LocalItemValidationError("word phonetic contains ordinary English spelling")
    if "'" in phonetic or '"' in phonetic:
        raise LocalItemValidationError("word phonetic uses ASCII quote instead of IPA stress mark")
    if ":" in phonetic:
        raise LocalItemValidationError("word phonetic uses ASCII colon instead of IPA length mark")
    return meaning, phonetic


def sentence_user_prompt(text: str) -> str:
    return text


def word_user_prompt(word: str) -> str:
    return word


def main() -> None:
    backend: LocalMLXBackend | None = None
    loaded_model = ""
    for raw in sys.stdin:
        raw = raw.strip()
        if not raw:
            continue
        try:
            request = json.loads(raw)
            model_id = str(request.get("model_id", "")).strip()
            if model_id != LOCAL_MODEL_ID:
                raise ValueError(f"unsupported local translation model: {model_id}")

            task = str(request.get("task", "")).strip()
            text = normalize_source_text(request.get("text", ""))
            if task not in {"sentence", "word"}:
                raise ValueError(f"unsupported local translation task: {task}")
            if not text:
                raise ValueError("local translation request text is empty")

            if backend is None or loaded_model != model_id:
                print(
                    json.dumps(
                        {"event": "status", "message": "正在加载本地 Qwen3-4B 翻译模型"},
                        ensure_ascii=False,
                    ),
                    flush=True,
                )
                backend = LocalMLXBackend()
                loaded_model = model_id

            if task == "sentence":
                backend.set_operation("本地逐条句子翻译", request_type="sentence_translation")
                result = backend.generate(SENTENCE_SYSTEM_PROMPT, sentence_user_prompt(text), 128)
                translation = parse_sentence_result(result)
                print(
                    json.dumps(
                        {"done": True, "task": task, "translation": translation, "model_id": model_id},
                        ensure_ascii=False,
                    ),
                    flush=True,
                )
                continue

            backend.set_operation("本地逐条单词翻译", request_type="word_translation")
            result = backend.generate(WORD_SYSTEM_PROMPT, word_user_prompt(text), 96)
            meaning, phonetic = parse_word_result(result, text)
            print(
                json.dumps(
                    {
                        "done": True,
                        "task": task,
                        "meaning": meaning,
                        "phonetic": phonetic,
                        "model_id": model_id,
                    },
                    ensure_ascii=False,
                ),
                flush=True,
            )
        except Exception as exc:
            print(
                json.dumps(
                    {
                        "done": True,
                        "error": str(exc),
                        "error_type": type(exc).__name__,
                        "recoverable": isinstance(exc, LocalItemValidationError),
                    },
                    ensure_ascii=False,
                ),
                flush=True,
            )


if __name__ == "__main__":
    main()
