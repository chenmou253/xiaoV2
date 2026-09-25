#!/usr/bin/env python3
"""Generate Qwen3-TTS audio with local QA and safe per-book word reuse."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import sys
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable

from common.audio_qa import AudioQAConfig, AudioQualityGate
from common.paths import validate_book_id
from common.tts_providers import CLOUD_VOICES, LOCAL_QWEN_MODELS, create_tts_provider

ACCENT_VOICE_IDS = {"en-US": "aiden", "en-GB": "ryan"}


def validate_voice_mapping(voices: dict[str, str], model_id: str = "local-qwen3-tts") -> None:
    for accent, voice in voices.items():
        if accent not in ACCENT_VOICE_IDS:
            raise ValueError(f"unsupported accent: {accent}")
        if model_id == "qwen3-tts-flash":
            if voice not in CLOUD_VOICES:
                raise ValueError(f"unsupported qwen3-tts-flash voice: {voice}")
            continue
        expected = ACCENT_VOICE_IDS.get(accent)
        if voice != expected:
            raise ValueError(
                f"{accent} is locked to Qwen3-TTS speaker {expected}; got {voice}")


def normalized_word(text: str) -> str:
    value = " ".join(text.strip().replace("‘", "'").replace("’", "'").split())
    value = re.sub(r"^[^\w]+|[^\w]+$", "", value, flags=re.UNICODE)
    return value.casefold() or text.strip().casefold()


def spoken_text(text: str) -> str:
    value = re.sub(r"_+|\*{2,}", ", blank, ", text).replace("/", ", ")
    if not any(character.isalnum() for character in value):
        return "blank" if "_" in text or "*" in text else "ellipsis"
    return "I." if value.strip().casefold() == "i" else value


def _safe_component(value: str, fallback: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9_-]+", "-", value).strip("-")[:64]
    digest = uuid.uuid5(uuid.NAMESPACE_URL, value).hex[:8]
    return f"{cleaned or fallback}-{digest}"


def _atomic_json(path: Path, value: dict) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    temporary.replace(path)


@dataclass(frozen=True)
class AudioItem:
    kind: str
    text: str
    context: str
    page: int
    segment_id: str
    item_id: str
    segment_index: int
    word_index: int | None
    accent: str
    voice: str


class PageAudioError(RuntimeError):
    def __init__(self, summary: dict):
        self.summary = summary
        failed = summary.get("failed_items", [])
        detail = "; ".join(
            f"{item['item_id']}/{item['accent']}/{item.get('voice', '-')}: {','.join(item['reasons'])}"
            for item in failed[:8]
        )
        super().__init__(f"音频 QA 未通过 {summary['failed']}/{summary['total']} 项；{detail}")


class AudioGenerator:
    """Own the heavy Qwen3-TTS and Whisper models and reuse them across pages."""

    def __init__(self, _legacy_model_dir: Path | None = None,
                 model_id: str = "local-qwen3-tts"):
        self.model_id = model_id
        self.engine = create_tts_provider(model_id)
        self.config = AudioQAConfig()
        self.gate = AudioQualityGate(self.config)
        print(
            "[TTS HEALTH] "
            f"provider={'dashscope' if self.engine.cloud else 'local'} "
            f"model={self.model_id} retry={self.engine.retry_policy}",
            file=sys.stderr,
            flush=True,
        )

    def _model(self) -> str:
        return str(getattr(self, "model_id", getattr(self.engine, "model_id", "local-qwen3-tts")))

    def _cloud(self) -> bool:
        return bool(getattr(self.engine, "cloud", False))

    def _word_cache_key(self, item: AudioItem) -> str:
        if item.kind != "word":
            return ""
        normalized = normalized_word(item.text)
        return hashlib.sha256(normalized.encode("utf-8")).hexdigest()

    def _sentence_cache_key(self, item: AudioItem) -> str:
        if item.kind != "sentence":
            return ""
        # Keep sentence reuse scoped to the same spoken text, model, accent,
        # and voice.
        # The cache itself lives under this book's tts directory.
        source = " ".join(spoken_text(item.text).split())
        value = "\0".join((source, self._model(), item.accent, item.voice))
        return hashlib.sha256(value.encode("utf-8")).hexdigest()

    @staticmethod
    def _reusable_word(manifest: dict, output: Path, cache_key: str,
                       word: str) -> dict | None:
        if not cache_key:
            return None
        output_root = output.resolve()
        for entry in manifest.get("items", []):
            if (entry.get("kind") != "word"
                    or entry.get("status", "ready") != "ready"
                    or (entry.get("word_cache_key") != cache_key
                        and normalized_word(str(entry.get("text", ""))) != word)
                    or not entry.get("file")
                    or not bool(entry.get("qa", {}).get("passed"))):
                continue
            target = (output / str(entry["file"])).resolve()
            try:
                target.relative_to(output_root)
            except ValueError:
                continue
            try:
                if target.is_file() and target.stat().st_size > 44:
                    return entry
            except OSError:
                continue
        return None

    @staticmethod
    def _promote_reusable_word(output: Path, entry: dict,
                               cache_key: str) -> dict:
        relative = Path("word-cache") / cache_key[:2] / f"{cache_key}.wav"
        if Path(str(entry["file"])) == relative:
            entry["word_cache_key"] = cache_key
            return entry
        source = output / str(entry["file"])
        target = output / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        temporary = target.with_name(f".{target.name}.{uuid.uuid4().hex}.tmp")
        shutil.copyfile(source, temporary)
        temporary.replace(target)
        entry["file"] = relative.as_posix()
        entry["word_cache_key"] = cache_key
        return entry

    def _reusable_sentence(self, manifest: dict, output: Path, cache_key: str,
                           item: AudioItem) -> dict | None:
        if not cache_key:
            return None
        output_root = output.resolve()
        source = " ".join(spoken_text(item.text).split())
        for entry in manifest.get("items", []):
            entry_source = " ".join(spoken_text(str(entry.get("text", ""))).split())
            candidate_key = entry.get("sentence_cache_key")
            if not candidate_key:
                value = "\0".join((entry_source, str(entry.get("tts_model", "")),
                                    str(entry.get("accent", "")), str(entry.get("voice", ""))))
                candidate_key = hashlib.sha256(value.encode("utf-8")).hexdigest()
            if (entry.get("kind") != "sentence"
                    or entry.get("status", "ready") != "ready"
                    or candidate_key != cache_key
                    or entry_source != source
                    or str(entry.get("tts_model", "")) != self._model()
                    or str(entry.get("accent", "")) != item.accent
                    or str(entry.get("voice", "")) != item.voice
                    or not entry.get("file")
                    or not bool(entry.get("qa", {}).get("passed"))):
                continue
            target = (output / str(entry["file"])).resolve()
            try:
                target.relative_to(output_root)
            except ValueError:
                continue
            try:
                if target.is_file() and target.stat().st_size > 44:
                    return entry
            except OSError:
                continue
        return None

    @staticmethod
    def _promote_reusable_sentence(output: Path, entry: dict,
                                   cache_key: str) -> dict:
        relative = Path("sentence-cache") / cache_key[:2] / f"{cache_key}.wav"
        if Path(str(entry["file"])) == relative:
            entry["sentence_cache_key"] = cache_key
            return entry
        source = output / str(entry["file"])
        target = output / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        temporary = target.with_name(f".{target.name}.{uuid.uuid4().hex}.tmp")
        shutil.copyfile(source, temporary)
        temporary.replace(target)
        entry["file"] = relative.as_posix()
        entry["sentence_cache_key"] = cache_key
        return entry

    @staticmethod
    def _replace_manifest_item(manifest: dict, item: AudioItem, entry: dict) -> None:
        manifest["items"] = [
            current for current in manifest.get("items", [])
            if not (
                int(current.get("page", 0)) == item.page
                and str(current.get("item_id", "")) == item.item_id
                and str(current.get("accent", "")) == item.accent
            )
        ]
        manifest["items"].append(entry)
        manifest["failures"] = [
            failure for failure in manifest.get("failures", [])
            if not (
                int(failure.get("page", 0)) == item.page
                and str(failure.get("item_id", "")) == item.item_id
                and str(failure.get("accent", "")) == item.accent
            )
        ]

    def _items(self, content: dict, page: int,
               voices: dict[str, str] | None = None) -> list[AudioItem]:
        if voices is None:
            voices = {
                "en-US": ACCENT_VOICE_IDS["en-US"],
                "en-GB": ACCENT_VOICE_IDS["en-GB"],
            }
        validate_voice_mapping(voices, self._model())
        items: list[AudioItem] = []
        for segment_index, segment in enumerate(content.get("segments", [])):
            segment_id = str(segment.get("id") or f"segment-{segment_index + 1}")
            sentence = str(segment.get("text", "")).strip()
            audio_mode = str(segment.get("audio_mode") or "sentence_and_words")
            if audio_mode == "none":
                continue
            if sentence and audio_mode != "word_only":
                for accent, voice in voices.items():
                    items.append(AudioItem("sentence", sentence, sentence, page, segment_id,
                                           segment_id, segment_index, None, accent, voice))
            occurrence: dict[str, int] = {}
            for word_position, word in enumerate(segment.get("words", [])):
                text = str(word.get("text", "")).strip()
                if not text or not any(character.isalnum() for character in text):
                    continue
                item_id = str(word.get("id") or f"{segment_id}-word-{word_position + 1}")
                normalized = normalized_word(text)
                context_index = occurrence.get(normalized, 0)
                occurrence[normalized] = context_index + 1
                for accent, voice in voices.items():
                    items.append(AudioItem("word", text, sentence, page, segment_id,
                                           item_id, segment_index, word_position, accent, voice))
        return items

    @staticmethod
    def _paths(output: Path, item: AudioItem, generation_id: str,
               word_cache_key: str = "", sentence_cache_key: str = "") -> tuple[Path, Path]:
        segment = f"{item.segment_index + 1:03d}-{_safe_component(item.segment_id, 'segment')}"
        accent = "us" if item.accent == "en-US" else "uk"
        if item.kind == "sentence":
            if sentence_cache_key:
                relative = Path("sentence-cache") / sentence_cache_key[:2] / f"{sentence_cache_key}.wav"
            else:
                relative = Path(f"page-{item.page:03d}") / "sentences" / segment / f"{accent}-{generation_id}.wav"
        elif word_cache_key:
            relative = Path("word-cache") / word_cache_key[:2] / f"{word_cache_key}.wav"
        else:
            word = f"{(item.word_index or 0) + 1:03d}-{_safe_component(item.item_id, 'word')}"
            relative = Path(f"page-{item.page:03d}") / "words" / segment / word / f"{accent}-{generation_id}.wav"
        return output / relative, relative

    def _page_reuse_key(self, item: AudioItem) -> tuple[str, str, str, str]:
        """Identify one TTS request that may be shared within the current page."""
        source = spoken_text(normalized_word(item.text) if item.kind == "word" else item.text)
        return item.kind, source, item.accent, item.voice

    def _synthesize(self, item: AudioItem, attempt: int, retry_variant: int = 0):
        source = spoken_text(normalized_word(item.text) if item.kind == "word" else item.text)
        samples, sample_rate, generation = self.engine.synthesize(
            source, item.kind, item.accent, attempt, retry_variant, item.voice,
        )
        pronunciation_control = {
            "method": "dashscope-qwen3-tts-flash" if self._cloud() else "qwen3-tts-custom-voice-instruct",
            "context": item.context,
            **generation,
        }
        # Qwen3-TTS does not expose phoneme timestamps. The shared quality gate
        # therefore uses Whisper word timestamps plus waveform energy.
        return samples, sample_rate, [], "", pronunciation_control, source, 1.0

    def generate_page(self, resource_root: Path, book_id: str, page: int,
                      emit: Callable[[dict], None] | None = None,
                      item_id: str = "", accent: str = "", retry_variant: int = 0,
                      voices: dict[str, str] | None = None,
                      mode: str = "replace-page") -> dict:
        import soundfile as sf

        emit = emit or (lambda _event: None)
        validate_book_id(book_id)
        resource_root = resource_root.expanduser().resolve()
        book_root = (resource_root / book_id).resolve()
        try:
            book_root.relative_to(resource_root)
        except ValueError as exc:
            raise ValueError("audio resource path escapes resource root") from exc
        metadata = book_root / "metadata" / "pages" / f"page-{page:03d}.json"
        if not metadata.is_file():
            raise ValueError(f"no generated page metadata found for page {page}")
        content = json.loads(metadata.read_text())
        output = book_root / "tts"
        qa_dir = output / "qa"
        failed_root = output / "audio_failed" / f"page-{page:03d}"
        output.mkdir(parents=True, exist_ok=True)
        qa_dir.mkdir(parents=True, exist_ok=True)
        failed_root.mkdir(parents=True, exist_ok=True)
        generation_id = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S") + "-" + uuid.uuid4().hex[:10]
        manifest_path = output / "manifest.json"
        existing = json.loads(manifest_path.read_text()) if manifest_path.exists() else {}
        manifest = existing if existing.get("schema_version") == 2 else {
            "schema_version": 2, "model": self._model(), "items": [],
            "failures": [],
        }
        manifest["model"] = self._model()
        if mode not in {"replace-page", "replace-item", "missing", "replace-accent", "retry-failed"}:
            raise ValueError(f"unsupported audio generation mode: {mode}")
        if voices is None:
            voices = {
                "en-US": ACCENT_VOICE_IDS["en-US"],
                "en-GB": ACCENT_VOICE_IDS["en-GB"],
            }
        validate_voice_mapping(voices, self._model())
        for configured_accent, voice in voices.items():
            if configured_accent not in {"en-US", "en-GB"}:
                raise ValueError(f"unsupported accent: {configured_accent}")
            if hasattr(self.engine, "supported_speakers") and voice not in self.engine.supported_speakers:
                raise ValueError(f"unsupported Qwen3-TTS speaker: {voice}")
        items = self._items(content, page, voices)
        if accent and accent not in {"en-US", "en-GB"}:
            raise ValueError("accent must be en-US or en-GB")
        if item_id:
            items = [item for item in items if item.item_id == item_id]
        if accent:
            items = [item for item in items if item.accent == accent]
        if (item_id or accent) and not items:
            raise ValueError(f"audio item not found: page={page}, item_id={item_id}, accent={accent}")
        def mapped_ready(item: AudioItem) -> bool:
            for entry in manifest.get("items", []):
                if (int(entry.get("page", 0)) == page
                        and str(entry.get("item_id", "")) == item.item_id
                        and str(entry.get("accent", "")) == item.accent
                        and str(entry.get("voice", "")) == item.voice
                        and entry.get("file")
                        and (output / str(entry["file"])).is_file()):
                    return True
            return False

        if mode == "missing":
            items = [item for item in items if not mapped_ready(item)]
        elif mode == "retry-failed":
            failed_keys = {
                (int(failure.get("page", 0)), str(failure.get("item_id", "")),
                 str(failure.get("accent", "")))
                for failure in manifest.get("failures", [])
            }
            items = [item for item in items
                     if (page, item.item_id, item.accent) in failed_keys]
        summary = {"page": page, "generation_id": generation_id, "total": len(items),
                   "passed": 0, "reused": 0, "failed": 0, "retried": 0,
                   "failed_items": []}
        # Deduplicate identical TTS requests inside this page. Different accents,
        # voices, and sentence/word modes stay independent.
        page_results: dict[tuple[str, str, str, str], dict] = {}
        qa_log_path = qa_dir / f"page-{page:03d}.jsonl"
        if items:
            emit({"event": "progress", "page": page, "progress": 0,
                  "total": len(items), "passed": 0, "failed": 0})
        for index, item in enumerate(items, 1):
            word_cache_key = self._word_cache_key(item)
            sentence_cache_key = self._sentence_cache_key(item)
            page_key = self._page_reuse_key(item)
            page_result = page_results.get(page_key)
            if page_result is not None:
                if page_result["status"] == "ready":
                    source_entry = page_result["entry"]
                    reused_qa = json.loads(json.dumps(source_entry.get("qa", {})))
                    reused_qa.update({
                        "passed": True, "reused": True, "page_reused": True,
                        "page": page, "segment_id": item.segment_id,
                        "item_id": item.item_id, "word_index": item.word_index,
                        "text": item.text, "context": item.context,
                        "accent": item.accent, "voice": item.voice,
                    })
                    entry = {
                        "page": page, "item_id": item.item_id,
                        "segment_id": item.segment_id, "word_index": item.word_index,
                        "kind": item.kind, "text": item.text, "context": item.context,
                        "accent": item.accent, "voice": item.voice,
                        "tts_model": self._model(),
                        "generation_id": source_entry.get("generation_id", ""),
                        "generation_version": source_entry.get("generation_version", ""),
                        "generated_at": source_entry.get("generated_at", ""),
                        "status": "ready", "file": source_entry["file"],
                        "reused_from": {
                            "page": source_entry.get("page"),
                            "item_id": source_entry.get("item_id"),
                            "generation_id": source_entry.get("generation_id", ""),
                        },
                        "qa": reused_qa,
                    }
                    if word_cache_key:
                        entry["word_cache_key"] = word_cache_key
                    if sentence_cache_key:
                        entry["sentence_cache_key"] = sentence_cache_key
                    self._replace_manifest_item(manifest, item, entry)
                    _atomic_json(manifest_path, manifest)
                    summary["passed"] += 1
                    summary["reused"] += 1
                    with qa_log_path.open("a", encoding="utf-8") as log:
                        log.write(json.dumps(reused_qa, ensure_ascii=False) + "\n")
                    emit({"event": "qa", "qa": reused_qa})
                    emit({"event": "progress", "page": page, "progress": index,
                          "total": len(items), "passed": summary["passed"],
                          "failed": summary["failed"], "reused": summary["reused"]})
                    continue

                source_failure = json.loads(json.dumps(page_result["failure"]))
                duplicate_failure = source_failure
                duplicate_failure.update({
                    "page": page, "segment_id": item.segment_id,
                    "item_id": item.item_id, "word_index": item.word_index,
                    "text": item.text, "context": item.context,
                    "accent": item.accent, "voice": item.voice,
                    "reused": True, "page_reused": True,
                })
                source_failed_file = str(source_failure.get("failed_file", ""))
                duplicate_failed_file = ""
                debug = failed_root / _safe_component(item.item_id, item.kind) / item.accent
                shutil.rmtree(debug, ignore_errors=True)
                debug.mkdir(parents=True, exist_ok=True)
                if source_failed_file:
                    source_failed_path = output / source_failed_file
                    if source_failed_path.is_file():
                        duplicate_failed_path = debug / f"{generation_id}-reused.wav"
                        shutil.copyfile(source_failed_path, duplicate_failed_path)
                        duplicate_failed_file = duplicate_failed_path.relative_to(output).as_posix()
                duplicate_failure["failed_file"] = duplicate_failed_file
                duplicate_failure["generation_version"] = generation_id
                duplicate_failure["generated_at"] = datetime.now(timezone.utc).isoformat()
                duplicate_failure["status"] = "failed"
                _atomic_json(debug / f"{generation_id}.json", duplicate_failure)
                manifest["failures"] = [
                    failure for failure in manifest.get("failures", [])
                    if not (
                        int(failure.get("page", 0)) == page
                        and str(failure.get("item_id", "")) == item.item_id
                        and str(failure.get("accent", "")) == item.accent
                    )
                ]
                manifest.setdefault("failures", []).append(duplicate_failure)
                _atomic_json(manifest_path, manifest)
                summary["failed"] += 1
                reasons = list(duplicate_failure.get("reasons", ["unknown_audio_failure"]))
                summary["failed_items"].append({
                    "item_id": item.item_id, "accent": item.accent,
                    "voice": item.voice, "reasons": reasons,
                })
                with qa_log_path.open("a", encoding="utf-8") as log:
                    log.write(json.dumps(duplicate_failure, ensure_ascii=False) + "\n")
                emit({"event": "qa", "qa": duplicate_failure})
                emit({"event": "progress", "page": page, "progress": index,
                      "total": len(items), "passed": summary["passed"],
                      "failed": summary["failed"], "reused": summary["reused"]})
                continue

            reusable = None
            if word_cache_key:
                reusable = self._reusable_word(
                    manifest, output, word_cache_key, normalized_word(item.text),
                )
            elif sentence_cache_key:
                reusable = self._reusable_sentence(
                    manifest, output, sentence_cache_key, item,
                )
            if reusable is not None:
                if word_cache_key:
                    reusable = self._promote_reusable_word(output, reusable, word_cache_key)
                else:
                    reusable = self._promote_reusable_sentence(output, reusable, sentence_cache_key)
                reused_qa = json.loads(json.dumps(reusable.get("qa", {})))
                reused_qa.update({
                    "passed": True, "reused": True,
                    "page": page, "segment_id": item.segment_id,
                    "item_id": item.item_id, "word_index": item.word_index,
                    "text": item.text, "context": item.context,
                    "accent": item.accent, "voice": item.voice,
                })
                entry = {
                    "page": page, "item_id": item.item_id,
                    "segment_id": item.segment_id, "word_index": item.word_index,
                    "kind": item.kind, "text": item.text, "context": item.context,
                    "accent": item.accent, "voice": item.voice,
                    "tts_model": self._model(),
                    "generation_id": reusable.get("generation_id", ""),
                    "generation_version": reusable.get("generation_version", ""),
                    "generated_at": reusable.get("generated_at", ""),
                    "status": "ready", "file": reusable["file"],
                    "reused_from": {
                        "page": reusable.get("page"),
                        "item_id": reusable.get("item_id"),
                        "generation_id": reusable.get("generation_id", ""),
                    },
                    "qa": reused_qa,
                }
                if word_cache_key:
                    entry["word_cache_key"] = word_cache_key
                if sentence_cache_key:
                    entry["sentence_cache_key"] = sentence_cache_key
                self._replace_manifest_item(manifest, item, entry)
                _atomic_json(manifest_path, manifest)
                summary["passed"] += 1
                summary["reused"] += 1
                with qa_log_path.open("a", encoding="utf-8") as log:
                    log.write(json.dumps(reused_qa, ensure_ascii=False) + "\n")
                emit({"event": "qa", "qa": reused_qa})
                emit({"event": "progress", "page": page, "progress": index,
                      "total": len(items), "passed": summary["passed"],
                      "failed": summary["failed"], "reused": summary["reused"]})
                page_results[page_key] = {"status": "ready", "entry": entry}
                continue
            # A word has one canonical audio file for the whole textbook.
            # When no approved reusable entry exists, temporary.replace()
            # atomically installs the newly generated WAV after QA succeeds,
            # so every existing reference hears the new pronunciation without
            # creating per-item duplicates.
            final_path, relative_path = self._paths(
                output, item, generation_id, word_cache_key, sentence_cache_key,
            )
            final_path.parent.mkdir(parents=True, exist_ok=True)
            passed_result: dict | None = None
            last_result: dict = {"reasons": ["generation_not_started"]}
            last_temp: Path | None = None
            max_attempts = 1 if self._cloud() else self.config.max_retry
            for attempt in range(1, max_attempts + 1):
                temporary = final_path.with_name(
                    f".{final_path.stem}.{generation_id}.{_safe_component(item.item_id, 'item')}.attempt-{attempt}.tmp.wav"
                )
                try:
                    samples, sample_rate, timings, phonemes, g2p, expected, speed = self._synthesize(item, attempt, retry_variant)
                    sf.write(str(temporary), samples, sample_rate, subtype="PCM_16")
                    quality = self.gate.check(temporary, samples, sample_rate, item.kind,
                                              expected, phonemes, timings, attempt)
                    quality.update({
                        "type": item.kind, "book_id": book_id, "page": page,
                        "segment_id": item.segment_id, "item_id": item.item_id,
                        "word_index": item.word_index, "text": item.text,
                        "context": item.context, "accent": item.accent,
                        "voice": item.voice, "generation_id": generation_id,
                        "tts_model": self._model(),
                        "provider": "dashscope" if self._cloud() else "local",
                        "request_id": g2p.get("request_id", ""),
                        "g2p": g2p, "speed": speed,
                        "generation": {key: value for key, value in g2p.items()
                                       if key not in {"method", "context"}},
                    })
                except Exception as exc:  # isolate this item and continue the page
                    detail = f"{type(exc).__name__}:{exc}"
                    if isinstance(exc, TimeoutError):
                        reason = "generation_timeout"
                    elif "duration_invalid" in str(exc):
                        reason = "duration_invalid"
                    else:
                        reason = "generation_failed"
                    quality = {
                        "passed": False, "attempt": attempt, "type": item.kind,
                        "book_id": book_id, "page": page, "segment_id": item.segment_id,
                        "item_id": item.item_id, "word_index": item.word_index,
                        "text": item.text, "context": item.context,
                        "accent": item.accent, "voice": item.voice,
                        "generation_id": generation_id,
                        "tts_model": self._model(),
                        "provider": "dashscope" if self._cloud() else "local",
                        "final_score": 0.0,
                        "reasons": [reason], "error": detail,
                    }
                generation = quality.get("generation", {})
                print(
                    "[TTS] "
                    f"book={book_id} page={page} type={item.kind} id={item.item_id} "
                    f"accent={item.accent} speaker={item.voice} attempt={attempt} "
                    f"text={json.dumps(item.text, ensure_ascii=False)} "
                    f"generation={generation.get('generation_seconds', 0)}s "
                    f"audio={quality.get('audio', {}).get('duration', generation.get('audio_duration', 0))}s "
                    f"quality={'PASS' if quality.get('passed') else 'FAIL'} "
                    f"reason={','.join(quality.get('reasons', [])) or '-'}",
                    file=sys.stderr,
                    flush=True,
                )
                with qa_log_path.open("a", encoding="utf-8") as log:
                    log.write(json.dumps(quality, ensure_ascii=False) + "\n")
                emit({"event": "qa", "qa": quality})
                last_result, last_temp = quality, temporary
                if quality.get("passed"):
                    temporary.replace(final_path)
                    passed_result = quality
                    break
                summary["retried"] += int(attempt < max_attempts)
                if temporary.exists() and attempt < max_attempts:
                    temporary.unlink()
            if passed_result:
                summary["passed"] += 1
                entry = {
                    "page": page, "item_id": item.item_id, "segment_id": item.segment_id,
                    "word_index": item.word_index, "kind": item.kind, "text": item.text,
                    "context": item.context, "accent": item.accent, "voice": item.voice,
                    "tts_model": self._model(),
                    "generation_id": generation_id,
                    "generation_version": generation_id,
                    "generated_at": datetime.now(timezone.utc).isoformat(),
                    "status": "ready",
                    "file": relative_path.as_posix(), "qa": passed_result,
                }
                if word_cache_key:
                    entry["word_cache_key"] = word_cache_key
                if sentence_cache_key:
                    entry["sentence_cache_key"] = sentence_cache_key
                self._replace_manifest_item(manifest, item, entry)
                _atomic_json(manifest_path, manifest)
                page_results[page_key] = {"status": "ready", "entry": entry}
                debug = failed_root / _safe_component(item.item_id, item.kind) / item.accent
                shutil.rmtree(debug, ignore_errors=True)
            else:
                summary["failed"] += 1
                reasons = list(last_result.get("reasons", ["unknown_audio_failure"]))
                summary["failed_items"].append({"item_id": item.item_id,
                                                "accent": item.accent, "voice": item.voice,
                                                "reasons": reasons})
                debug = failed_root / _safe_component(item.item_id, item.kind) / item.accent
                shutil.rmtree(debug, ignore_errors=True)
                debug.mkdir(parents=True, exist_ok=True)
                failed_file = ""
                if last_temp and last_temp.exists():
                    failed_path = debug / f"{generation_id}-attempt-{max_attempts}.wav"
                    last_temp.replace(failed_path)
                    failed_file = failed_path.relative_to(output).as_posix()
                last_result["failed_file"] = failed_file
                last_result["generation_version"] = generation_id
                last_result["generated_at"] = datetime.now(timezone.utc).isoformat()
                last_result["status"] = "failed"
                _atomic_json(debug / f"{generation_id}.json", last_result)
                if mode == "replace-item":
                    manifest["items"] = [
                        current for current in manifest.get("items", [])
                        if not (
                            int(current.get("page", 0)) == page
                            and str(current.get("item_id", "")) == item.item_id
                            and str(current.get("accent", "")) == item.accent
                        )
                    ]
                manifest["failures"] = [
                    failure for failure in manifest.get("failures", [])
                    if not (
                        int(failure.get("page", 0)) == page
                        and str(failure.get("item_id", "")) == item.item_id
                        and str(failure.get("accent", "")) == item.accent
                    )
                ]
                manifest.setdefault("failures", []).append(last_result)
                _atomic_json(manifest_path, manifest)
                page_results[page_key] = {"status": "failed", "failure": json.loads(json.dumps(last_result))}
            emit({"event": "progress", "page": page, "progress": index,
                  "total": len(items), "passed": summary["passed"],
                  "failed": summary["failed"]})
        emit({"event": "summary", **summary})
        if summary["failed"]:
            raise PageAudioError(summary)
        return summary


def _cli_emit(event: dict) -> None:
    if event.get("event") == "qa":
        qa = event["qa"]
        tag = "AUDIO QA" if qa.get("passed") else "AUDIO QA FAIL"
        print(f"[{tag}] " + json.dumps(qa, ensure_ascii=False), flush=True)
    else:
        print(json.dumps(event, ensure_ascii=False), flush=True)


def daemon() -> None:
    generator: AudioGenerator | None = None
    for line in sys.stdin:
        try:
            request = json.loads(line)
            model_id = str(request.get("model_id") or "local-qwen3-tts")
            if generator is not None and generator.model_id != model_id:
                raise RuntimeError(
                    f"MODEL_SWITCH_REQUIRED:{generator.model_id}->{model_id}"
                )
            if generator is None:
                # model_dir remains accepted in the request for Go/API
                # compatibility. A daemon owns exactly one selected model.
                generator = AudioGenerator(model_id=model_id)
            summary = generator.generate_page(
                Path(request["resource_root"]), request["book_id"], int(request["page"]),
                emit=lambda event: print(json.dumps(event, ensure_ascii=False), flush=True),
                item_id=str(request.get("item_id", "")),
                accent=str(request.get("accent", "")),
                retry_variant=int(request.get("retry_variant", 0)),
                voices=({str(key): str(value) for key, value in request["voices"].items()}
                        if "voices" in request else None),
                mode=str(request.get("mode", "replace-page")),
            )
            print(json.dumps({"done": True, "summary": summary}, ensure_ascii=False), flush=True)
        except PageAudioError as exc:
            print(json.dumps({"done": True, "error": str(exc), "summary": exc.summary},
                             ensure_ascii=False), flush=True)
        except Exception as exc:
            print(json.dumps({"done": True, "error": f"{type(exc).__name__}: {exc}"},
                             ensure_ascii=False), flush=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--book-id", required=True)
    parser.add_argument("--model-dir", type=Path,
                        help="legacy compatibility option; Qwen uses the Hugging Face cache")
    parser.add_argument("--model-id", choices=tuple(LOCAL_QWEN_MODELS) + ("qwen3-tts-flash",),
                        default="local-qwen3-tts")
    parser.add_argument("--page", type=int, required=True)
    parser.add_argument("--item-id", default="")
    parser.add_argument("--accent", choices=("en-US", "en-GB"), default="")
    parser.add_argument("--retry-variant", type=int, default=0)
    parser.add_argument("--voice-us", default="")
    parser.add_argument("--voice-uk", default="")
    parser.add_argument("--disable-us", action="store_true")
    parser.add_argument("--disable-uk", action="store_true")
    parser.add_argument("--mode", choices=("replace-page", "replace-item", "missing", "replace-accent", "retry-failed"), default="replace-page")
    parser.add_argument("--resource-root", type=Path,
                        default=Path(os.getenv("RESOURCE_ROOT", "storage/books")))
    parser.add_argument("--force", action="store_true",
                        help="retained for compatibility; generation is always fresh")
    args = parser.parse_args()
    generator = AudioGenerator(args.model_dir.expanduser().resolve() if args.model_dir else None,
                               model_id=args.model_id)
    voices = {}
    if not args.disable_us:
        voices["en-US"] = args.voice_us or ACCENT_VOICE_IDS["en-US"]
    if not args.disable_uk:
        voices["en-GB"] = args.voice_uk or ACCENT_VOICE_IDS["en-GB"]
    generator.generate_page(args.resource_root, args.book_id, args.page, _cli_emit,
                            item_id=args.item_id, accent=args.accent,
                            retry_variant=args.retry_variant, voices=voices,
                            mode=args.mode)


if __name__ == "__main__":
    if "--daemon" in sys.argv:
        daemon()
    else:
        try:
            main()
        except PageAudioError as exc:
            raise SystemExit(str(exc)) from exc
