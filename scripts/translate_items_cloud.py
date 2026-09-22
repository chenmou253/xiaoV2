#!/usr/bin/env python3
"""Compact cloud translation protocol for textbook_translation_items.

Input:
{"sentences":["..."],"words":["..."]}

Output:
{"translations":["..."],"words":[["meaning","phonetic"]]}
"""
from __future__ import annotations
import argparse, json
from pathlib import Path
from typing import Any
from translate_page import configured_backend, OnlineLLMClient, clean_phonetic, clean_translation

SENTENCE_SYSTEM = """Translate English textbook sentences into accurate, natural Simplified Chinese suitable for primary-school students.
Preserve meaning, names, numbers and negation. Do not add or omit information."""

WORD_SYSTEM = """Translate each English word independently into one concise Simplified Chinese dictionary meaning suitable for primary-school students.
Also return General American English IPA for every word.
Rules:
- Treat every word independently. Never infer sentence context.
- Preserve input order.
- Use rhotic General American pronunciation.
- Use American /oʊ/ rather than British /əʊ/ where applicable.
- Include lexical stress where appropriate."""

SENTENCE_SCHEMA: dict[str, Any] = {
    "type":"object","additionalProperties":False,
    "properties":{"t":{"type":"array","items":{"type":"string"}}},
    "required":["t"],
}
WORD_SCHEMA: dict[str, Any] = {
    "type":"object","additionalProperties":False,
    "properties":{"r":{"type":"array","items":{
        "type":"array","prefixItems":[{"type":"string"},{"type":"string"}],
        "minItems":2,"maxItems":2
    }}},
    "required":["r"],
}

def call(client: OnlineLLMClient, system: str, payload: dict[str, Any], schema: dict[str, Any], name: str, max_tokens: int) -> dict[str, Any]:
    raw = client.generate(
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
            res=call(backend,SENTENCE_SYSTEM,{"s":sentences},SENTENCE_SCHEMA,"sentence_translations",max(256,64+len(sentences)*64))
            vals=res.get("t")
            if not isinstance(vals,list) or len(vals)!=len(sentences):
                raise ValueError("sentence translation result count mismatch")
            cleaned=[clean_translation(x) for x in vals]
            if any(not x for x in cleaned):
                raise ValueError("sentence translation contains an empty result")
            out["translations"]=cleaned
        if words:
            res=call(backend,WORD_SYSTEM,{"w":words},WORD_SCHEMA,"word_translations",max(512,96+len(words)*40))
            vals=res.get("r")
            if not isinstance(vals,list) or len(vals)!=len(words):
                raise ValueError("word translation result count mismatch")
            cleaned=[]
            for row in vals:
                if not isinstance(row,list) or len(row)!=2:
                    raise ValueError("invalid word translation row")
                meaning=clean_translation(row[0])
                phonetic=clean_phonetic(row[1])
                if not meaning or not phonetic:
                    raise ValueError("word translation contains incomplete meaning/phonetic")
                cleaned.append([meaning,phonetic])
            out["words"]=cleaned
        args.output.write_text(json.dumps(out,ensure_ascii=False,separators=(",",":"))+"\n",encoding="utf-8")
    finally:
        logger=getattr(backend,"log_summary",None)
        if callable(logger): logger()

if __name__=="__main__":
    main()
