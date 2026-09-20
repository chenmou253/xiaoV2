#!/usr/bin/env python3
"""Slow, real Qwen3-TTS + faster-whisper integration and stability suite."""
from __future__ import annotations

import argparse
import json
import resource
import statistics
import time
from pathlib import Path

import soundfile as sf

from common.audio_quality import analyze_audio
from generate_audio import AudioGenerator, PageAudioError


FIXED_SENTENCE = "The little rabbit is running in the garden."
CASES = [
    ("A-normal", "sentence", "The cat is sleeping on the sofa."),
    ("B-word", "word", "elephant"),
    ("C-number", "sentence", "There are 30 students in the classroom."),
    ("D-punctuation", "sentence", "Hello! How are you?"),
    ("E-abbreviation", "sentence", "Mr. Smith is my English teacher."),
    ("F-long", "sentence", "When the little boy arrived at school, his friends were already waiting for him near the classroom."),
    ("G-legal-repeat", "sentence", "Very, very good!"),
]


def _fixed_page(root: Path, generator: AudioGenerator) -> dict:
    metadata = root / "qa-book" / "metadata" / "pages"
    metadata.mkdir(parents=True, exist_ok=True)
    metadata.joinpath("page-001.json").write_text(json.dumps({
        "book_id": "qa-book",
        "page": 1,
        "segments": [{
            "id": "fixed-rabbit",
            "text": FIXED_SENTENCE,
            "words": [{"id": "fixed-rabbit-word", "text": "rabbit"}],
        }],
    }, ensure_ascii=False, indent=2) + "\n")

    def emit(event: dict) -> None:
        if event.get("event") == "qa":
            qa = event["qa"]
            print("[FIXED] " + json.dumps({
                "result": "PASS" if qa.get("passed") else "FAIL",
                "type": qa.get("type"), "text": qa.get("text"),
                "accent": qa.get("accent"), "speaker": qa.get("voice"),
                "recognized": qa.get("asr", {}).get("recognized"),
                "duration": qa.get("audio", {}).get("duration"),
                "generation": qa.get("generation", {}).get("generation_seconds"),
                "rtf": qa.get("generation", {}).get("rtf"),
                "reasons": qa.get("reasons"),
            }, ensure_ascii=False), flush=True)

    try:
        return generator.generate_page(root, "qa-book", 1, emit)
    except PageAudioError as exc:
        return exc.summary


def _qa_cases(root: Path, generator: AudioGenerator) -> list[dict]:
    results: list[dict] = []
    case_dir = root / "case-a-h"
    case_dir.mkdir(parents=True, exist_ok=True)
    for name, kind, text in CASES:
        passed = False
        last: dict = {}
        for attempt in range(1, generator.config.max_retry + 1):
            samples, rate, generation = generator.engine.synthesize(text, kind, "en-US", attempt)
            wav = case_dir / f"{name}-attempt-{attempt}.wav"
            sf.write(wav, samples, rate, subtype="PCM_16")
            last = generator.gate.check(wav, samples, rate, kind, text, "", [], attempt)
            last["generation"] = generation
            print("[CASE] " + json.dumps({
                "case": name, "attempt": attempt,
                "result": "PASS" if last.get("passed") else "FAIL",
                "recognized": last.get("asr", {}).get("recognized"),
                "duration": last.get("audio", {}).get("duration"),
                "generation": generation["generation_seconds"],
                "rtf": generation["rtf"], "reasons": last.get("reasons"),
            }, ensure_ascii=False), flush=True)
            if last.get("passed"):
                passed = True
                break
        results.append({"case": name, "passed": passed, "qa": last})
    return results


def _continuous(generator: AudioGenerator, count: int) -> dict:
    sentence_times: list[float] = []
    word_times: list[float] = []
    rtfs: list[float] = []
    model_peaks: list[float] = []
    memory_samples: list[dict] = []
    failures: list[dict] = []
    retries = 0
    stability_texts = CASES + [("H-rabbit", "word", "rabbit")]
    started = time.perf_counter()
    for index in range(count):
        name, kind, text = stability_texts[index % len(stability_texts)]
        accent = "en-US" if index % 2 == 0 else "en-GB"
        passed = False
        last_reasons: list[str] = []
        for attempt in range(1, generator.config.max_retry + 1):
            try:
                samples, rate, generation = generator.engine.synthesize(text, kind, accent, attempt)
                waveform = analyze_audio(samples, rate, kind, text)
                last_reasons = list(waveform["reasons"])
                target = word_times if kind == "word" else sentence_times
                target.append(float(generation["generation_seconds"]))
                rtfs.append(float(generation["rtf"]))
                model_peaks.append(float(generation["peak_memory_gb"]))
                if waveform["passed"]:
                    passed = True
                    break
            except Exception as exc:
                last_reasons = [f"{type(exc).__name__}:{exc}"]
            retries += int(attempt < generator.config.max_retry)
        if not passed:
            failures.append({"index": index + 1, "case": name, "reasons": last_reasons})
        memory_samples.append(generator.engine.memory_usage())
        print(f"[STABILITY] {index + 1}/{count}", flush=True)
    # On macOS ru_maxrss is bytes.
    process_peak_gb = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / (1024 ** 3)
    return {
        "count": count,
        "elapsed_seconds": round(time.perf_counter() - started, 3),
        "average_sentence_seconds": round(statistics.fmean(sentence_times), 4) if sentence_times else None,
        "average_word_seconds": round(statistics.fmean(word_times), 4) if word_times else None,
        "average_rtf": round(statistics.fmean(rtfs), 4) if rtfs else None,
        "model_peak_memory_gb": round(max(model_peaks, default=0.0), 4),
        "process_peak_memory_gb": round(process_peak_gb, 4),
        "active_memory_start_gb": memory_samples[0]["active_gb"] if memory_samples else None,
        "active_memory_end_gb": memory_samples[-1]["active_gb"] if memory_samples else None,
        "cache_memory_end_gb": memory_samples[-1]["cache_gb"] if memory_samples else None,
        "retries": retries,
        "failures": failures,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path,
                        default=Path("/private/tmp/xiaov2-qwen-tts-integration"))
    parser.add_argument("--continuous-count", type=int, default=30)
    parser.add_argument("--fixed-only", action="store_true")
    parser.add_argument("--continuous-only", action="store_true")
    args = parser.parse_args()
    output = args.output.expanduser().resolve()
    output.mkdir(parents=True, exist_ok=True)

    generator = AudioGenerator()
    report = {
        "model": generator.engine.model_id,
        "model_cache": str(generator.engine.model_cache),
        "supported_speakers": generator.engine.supported_speakers,
        "american_speaker": generator.engine.american_speaker,
        "british_speaker": generator.engine.british_speaker,
        "model_load_seconds": round(generator.engine.load_seconds, 4),
        "warmup_seconds": round(generator.engine.warmup_seconds, 4),
    }
    if not args.continuous_only:
        report["fixed"] = _fixed_page(output, generator)
    if not args.fixed_only and not args.continuous_only:
        report["cases"] = _qa_cases(output, generator)
    if not args.fixed_only:
        report["continuous"] = _continuous(generator, max(30, min(50, args.continuous_count)))
    report_path = output / "qwen-tts-integration-report.json"
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print("FINAL " + json.dumps(report, ensure_ascii=False), flush=True)
    failed = report.get("fixed", {}).get("failed", 0) > 0
    failed = failed or any(not item["passed"] for item in report.get("cases", []))
    failed = failed or bool(report.get("continuous", {}).get("failures"))
    print(f"Report: {report_path}", flush=True)
    raise SystemExit(1 if failed else 0)


if __name__ == "__main__":
    main()
