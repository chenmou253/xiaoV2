#!/usr/bin/env python3
"""Cloud translation protocol for textbook_translation_items.

Sentences stay batched, but every row carries an explicit index and source text
so response ordering is never trusted. Words are requested one at a time, which
removes cross-word result shifting entirely.

Input:
{"sentences":["..."],"words":["..."]}

Output:
{"translations":["..."],"words":[["meaning","phonetic"]]}
"""
from __future__ import annotations
import argparse, json, unicodedata
from pathlib import Path
from typing import Any
from translate_page import configured_backend, OnlineLLMClient, clean_translation

SENTENCE_SYSTEM = """Translate English textbook sentences into accurate, natural Simplified Chinese suitable for primary-school students.
For every input row, copy "i" and "s" exactly and add "t" with the Chinese translation.
Do not reorder rows. Preserve meaning, names, numbers and negation. Do not add or omit information."""

WORD_SYSTEM = """Translate exactly ONE English word independently, without sentence context.
Return:
- "m": one concise Simplified Chinese dictionary meaning suitable for primary-school students.
- "p": General American English IPA only.

Rules:
- "m" MUST contain Chinese meaning, never IPA.
- "p" MUST contain IPA, never Chinese translation.
- Ignore surrounding punctuation when determining pronunciation and meaning.
- Use rhotic General American pronunciation.
- Use American /oʊ/ rather than British /əʊ/ where applicable.
- Include lexical stress where appropriate.
- Do not return another word's meaning or pronunciation."""

def normalize_cloud_word(value: Any) -> str:
    """Remove surrounding punctuation before sending one word to the cloud LLM.

    Internal punctuation such as the apostrophe in "children's" is preserved.
    The database source_text is not changed.
    """
    text = str(value or "").strip()
    start, end = 0, len(text)
    while start < end and unicodedata.category(text[start]).startswith("P"):
        start += 1
    while end > start and unicodedata.category(text[end - 1]).startswith("P"):
        end -= 1
    return text[start:end].strip()


def exact_array_schema(key: str, item_schema: dict[str, Any], count: int) -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            key: {
                "type": "array",
                "items": item_schema,
                "minItems": count,
                "maxItems": count,
            }
        },
        "required": [key],
    }

def call(client: OnlineLLMClient, system: str, payload: dict[str, Any], schema: dict[str, Any], name: str, max_tokens: int) -> dict[str, Any]:
    raw = client.generate_structured(
        system,
        json.dumps(payload, ensure_ascii=False, separators=(",", ":")),
        max_tokens,
        schema=schema,
        schema_name=name,
    )
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError("cloud translation response must be an object")
    return value

def main() -> None:
    ap=argparse.ArgumentParser()
    ap.add_argument("--input",type=Path,required=True)
    ap.add_argument("--output",type=Path,required=True)
    ap.add_argument("--model-id",required=True)
    args=ap.parse_args()
    data=json.loads(args.input.read_text(encoding="utf-8"))
    sentences=[str(x).strip() for x in data.get("sentences",[]) if str(x).strip()]
    words=[str(x).strip() for x in data.get("words",[]) if str(x).strip()]
    backend=configured_backend(args.model_id)
    if not isinstance(backend, OnlineLLMClient):
        raise RuntimeError("compact cloud translator requires an online model")
    out={"translations":[],"words":[]}
    try:
        if sentences:
            sentence_row_schema = {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "i": {"type": "integer"},
                    "s": {"type": "string"},
                    "t": {"type": "string"},
                },
                "required": ["i", "s", "t"],
            }
            sentence_schema = exact_array_schema("r", sentence_row_schema, len(sentences))
            sentence_input = [{"i": index, "s": source} for index, source in enumerate(sentences)]
            res = call(
                backend,
                SENTENCE_SYSTEM,
                {"r": sentence_input},
                sentence_schema,
                "sentence_translations",
                max(256, 96 + len(sentences) * 80),
            )
            vals = res.get("r")
            if not isinstance(vals, list) or len(vals) != len(sentences):
                raise ValueError("sentence translation result count mismatch")
            translations: list[str | None] = [None] * len(sentences)
            seen_indexes: set[int] = set()
            for row in vals:
                if not isinstance(row, dict):
                    raise ValueError(f"invalid sentence translation row: {row!r}")
                index = row.get("i")
                if not isinstance(index, int) or index < 0 or index >= len(sentences):
                    raise ValueError(f"invalid sentence translation index: {index!r}")
                if index in seen_indexes:
                    raise ValueError(f"duplicate sentence translation index: {index}")
                seen_indexes.add(index)
                returned_source = str(row.get("s", "")).strip()
                if returned_source != sentences[index]:
                    raise ValueError(
                        "sentence translation source mismatch: "
                        f"index={index}, expected={sentences[index]!r}, returned={returned_source!r}"
                    )
                # Empty sentence translations are intentionally preserved. The
                # service layer routes them to manual review.
                translations[index] = clean_translation(row.get("t", ""))
            if any(value is None for value in translations):
                raise ValueError("sentence translation response is missing an index")
            out["translations"] = [value or "" for value in translations]

        if words:
            word_schema = {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "m": {"type": "string", "minLength": 1},
                    "p": {"type": "string", "minLength": 1},
                },
                "required": ["m", "p"],
            }
            cleaned = []
            for index, source in enumerate(words):
                # Keep source_text unchanged in the database, but remove
                # surrounding OCR/textbook punctuation before the cloud request.
                request_word = normalize_cloud_word(source)
                if not request_word:
                    raise ValueError(f"word is empty after punctuation cleanup: index={index}, word={source!r}")
                # One source word per API request. There is no batch array whose
                # rows can shift and silently attach another word's result.
                row = call(
                    backend,
                    WORD_SYSTEM,
                    {"w": request_word},
                    word_schema,
                    "word_translation",
                    160,
                )
                raw_meaning = row.get("m", "")
                raw_phonetic = row.get("p", "")
                meaning = clean_translation(raw_meaning)
                # Do not validate or rewrite Qwen's IPA here. Local Review is the
                # quality gate; preserve the cloud model's value except whitespace.
                phonetic = str(raw_phonetic or "").strip()
                if not meaning or not phonetic:
                    raise ValueError(
                        "word translation contains incomplete meaning/phonetic: "
                        f"index={index}, word={source!r}, request_word={request_word!r}, "
                        f"raw_meaning={raw_meaning!r}, raw_phonetic={raw_phonetic!r}, "
                        f"meaning={meaning!r}, phonetic={phonetic!r}"
                    )
                cleaned.append([meaning, phonetic])
            out["words"] = cleaned
        args.output.write_text(json.dumps(out,ensure_ascii=False,separators=(",",":"))+"\n",encoding="utf-8")
    finally:
        logger=getattr(backend,"log_summary",None)
        if callable(logger): logger()

if __name__=="__main__":
    main()
