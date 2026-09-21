#!/usr/bin/env python3
"""Resident local translation daemon for xiaoV2.

Protocol: one JSON request per stdin line. A request contains:
  input, output, model_id
The daemon keeps one MLX model resident and emits translate_page progress events
plus a terminal {"done": true} frame.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

from translate_page import LocalMLXBackend, translate_page

LOCAL_MODEL_ID = "local-qwen3-4b-instruct-2507"


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
            source = Path(str(request["input"]))
            output = Path(str(request["output"]))
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
            content = json.loads(source.read_text(encoding="utf-8"))
            translated = translate_page(content, backend)
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text(
                json.dumps(translated, ensure_ascii=False, indent=2) + "\n",
                encoding="utf-8",
            )
            print(json.dumps({"done": True, "model_id": model_id}), flush=True)
        except Exception as exc:
            print(
                json.dumps(
                    {"done": True, "error": str(exc), "error_type": type(exc).__name__},
                    ensure_ascii=False,
                ),
                flush=True,
            )


if __name__ == "__main__":
    main()
