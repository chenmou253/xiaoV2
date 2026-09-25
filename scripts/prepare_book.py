#!/usr/bin/env python3
"""Convert one PDF into the canonical, book_id-isolated resource tree.

This script creates only filesystem resources. MySQL remains the catalogue's
source of truth; import the reviewed manifest with ``go run ./cmd/bookctl``.
"""
from __future__ import annotations

import argparse
import json
import re
import shutil
import statistics
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path

from common import paths as resource_paths
from common.ocr import PaddleOCREngine, text_layer_is_reliable
from common.ocr_providers import LocalPaddleOCRProvider, create_ocr_provider
from common.paths import ensure_book_tree, page_stem, validate_book_id


def run(*args: str, timeout: int = 180) -> bytes:
    return subprocess.run(args, check=True, capture_output=True, timeout=timeout).stdout


def render_page_images(pdf: Path, page: int, stem: str, dirs: dict[str, Path]) -> tuple[Path, Path]:
    """Render a temporary PNG for OCR and persist only a quality-82 WebP page."""
    ocr_image = dirs["cache"] / f"{stem}.ocr.png"
    webp_image = dirs["pages"] / f"{stem}.webp"
    ocr_image.unlink(missing_ok=True)
    webp_image.unlink(missing_ok=True)
    run("pdftoppm", "-f", str(page), "-l", str(page), "-r", "300",
        "-singlefile", "-png", str(pdf), str(ocr_image.with_suffix("")))
    run("cwebp", "-quiet", "-q", "82", str(ocr_image), "-o", str(webp_image))
    return ocr_image, webp_image


def text_layer(pdf: Path, page: int) -> list[dict]:
    if not shutil.which("pdftotext"):
        return []
    root = ET.fromstring(run("pdftotext", "-f", str(page), "-l", str(page),
                             "-bbox-layout", str(pdf), "-"))
    pages = list(root.iter("{http://www.w3.org/1999/xhtml}page"))
    if not pages:
        return []
    source = pages[0]
    width, height = float(source.attrib["width"]), float(source.attrib["height"])
    rows = []
    for line in source.iter("{http://www.w3.org/1999/xhtml}line"):
        words = []
        for word in line:
            x, y, right, bottom = [float(word.attrib[key]) for key in ("xMin", "yMin", "xMax", "yMax")]
            if right > x >= 0 and bottom > y >= 0 and right <= width and bottom <= height:
                words.append({"text": word.text or "", "confidence": 1.0,
                              "needs_review": False, "box": [x / width, y / height,
                              (right - x) / width, (bottom - y) / height]})
        if words:
            left = min(word["box"][0] for word in words)
            top = min(word["box"][1] for word in words)
            right = max(word["box"][0] + word["box"][2] for word in words)
            bottom = max(word["box"][1] + word["box"][3] for word in words)
            # These coordinates use the source PDF's page rectangle. Poppler
            # renders that same rectangle into the final WebP without rotating
            # it, so normalized boxes land on the exact rendered page.
            rows.append({"text": " ".join(word["text"] for word in words), "confidence": 1.0,
                         "needs_review": False, "box": [left, top, right - left, bottom - top],
                         "words": words})
    return rows


_SENTENCE_END = re.compile(r"[.!?\u2026\u3002\uff01\uff1f][\"')\]\u201d\u2019]*$")
_BULLET_OR_ITEM = re.compile(r"^\s*(?:[\u2022\u25e6\-\u2013*]|\d+[.)])\s+")
_HARD_SENTENCE_END = re.compile(r"[!?\u2026\u3002\uff01\uff1f][\"')\]\u201d\u2019]*$")
_DOT_SENTENCE_END = re.compile(r"\.[\"')\]\u201d\u2019]*$")
_COMMON_ABBREVIATIONS = {
    "mr", "mrs", "ms", "dr", "prof", "sr", "jr", "st", "vs", "etc",
    "e.g", "i.e", "a.m", "p.m", "no", "fig", "inc", "ltd",
}
_COMMON_OCR_TOKEN_FIXES = {
    "l'm": "I'm", "l've": "I've", "l'll": "I'll", "l'd": "I'd",
    "0f": "of", "t0": "to",
}
_OPENING_SYMBOLS = set("([{${\u00a3\u20ac\u00a5")
_APOSTROPHES = {"'", "\u2019"}
_HYPHENS = {"-", "\u2010", "\u2011"}


def clean_ocr_text(value: object) -> str:
    """Apply only safe OCR cleanup; keep textbook punctuation and blanks."""
    text = str(value or "")
    text = (text.replace("\u2018", "'").replace("\u2019", "'")
                .replace("\u201c", '"').replace("\u201d", '"')
                .replace("\u2026", "...").replace("\u00a0", " "))
    # Do not collapse underscore runs: they are fill-in-the-blank content.
    text = re.sub(r"[\t\r\n ]+", " ", text).strip()
    return _COMMON_OCR_TOKEN_FIXES.get(text, text)


def row_box(row: dict, words: list[dict]) -> list[float] | None:
    box = row.get("box")
    if isinstance(box, list) and len(box) == 4:
        return [float(value) for value in box]
    if not words:
        return None
    left = min(float(word["box"][0]) for word in words)
    top = min(float(word["box"][1]) for word in words)
    right = max(float(word["box"][0]) + float(word["box"][2]) for word in words)
    bottom = max(float(word["box"][1]) + float(word["box"][3]) for word in words)
    return [left, top, right - left, bottom - top]


def join_ocr_words(words: list[dict]) -> str:
    """Rebuild readable text while retaining each OCR token's own box."""
    value = ""
    attach_to_previous = set(".,!?;:…。！？，、)]}”’%")
    for word in words:
        token = clean_ocr_text(word.get("text", ""))
        if not token:
            continue
        if not value:
            value = token
        elif token in attach_to_previous or token.startswith(("'", "’")) or value.endswith(("'", "’", "“", "(")):
            value += token
        else:
            value += " " + token
    return clean_ocr_text(value)


def _has_word_character(text: str) -> bool:
    return any(character.isalnum() or character == "_" for character in text)


def _merge_word_parts(parts: list[dict], text: str) -> dict:
    """Merge token text and geometry without losing OCR review metadata."""
    boxes = [part["box"] for part in parts if isinstance(part.get("box"), list)]
    left = min(box[0] for box in boxes)
    top = min(box[1] for box in boxes)
    right = max(box[0] + box[2] for box in boxes)
    bottom = max(box[1] + box[3] for box in boxes)
    merged = {"text": text, "box": [round(value, 6) for value in
                                      (left, top, right - left, bottom - top)]}
    confidences = [part.get("ocr_confidence") for part in parts
                   if isinstance(part.get("ocr_confidence"), (int, float))]
    if confidences:
        merged["ocr_confidence"] = min(confidences)
    if any(bool(part.get("ocr_needs_review")) for part in parts):
        merged["ocr_needs_review"] = True
    return merged


def merge_detached_symbols(words: list[dict]) -> list[dict]:
    """Attach standalone punctuation/symbols to words, never expose symbol boxes.

    The token text remains in the resulting word (and therefore in the full
    sentence sent to translation/TTS), while its box is unioned with a nearby
    lexical token. Detached apostrophes and hyphens between words are joined
    into one contraction/hyphenated word.
    """
    result: list[dict] = []
    pending_prefix: list[dict] = []
    quote_open = False
    index = 0
    while index < len(words):
        current = words[index]
        text = clean_ocr_text(current.get("text", ""))
        if not text:
            index += 1
            continue

        if _has_word_character(text):
            parts = [*pending_prefix, current]
            merged_text = "".join(clean_ocr_text(part.get("text", "")) for part in parts)
            pending_prefix.clear()

            # OCR often isolates apostrophes/hyphens as their own boxes.
            if (index + 2 < len(words)
                    and clean_ocr_text(words[index + 1].get("text", "")) in (_APOSTROPHES | _HYPHENS)
                    and _has_word_character(clean_ocr_text(words[index + 2].get("text", "")))):
                separator = words[index + 1]
                following = words[index + 2]
                merged = _merge_word_parts(
                    [*parts, separator, following],
                    merged_text + clean_ocr_text(separator.get("text", ""))
                    + clean_ocr_text(following.get("text", "")),
                )
                result.append(merged)
                index += 3
                continue

            result.append(_merge_word_parts(parts, merged_text))
            index += 1
            continue

        if text == '"':
            quote_open = not quote_open
            if quote_open and index + 1 < len(words):
                pending_prefix.append(current)
            elif result:
                previous = result.pop()
                result.append(_merge_word_parts(
                    [previous, current], previous["text"] + text,
                ))
            elif index + 1 < len(words):
                pending_prefix.append(current)
            index += 1
            continue

        # Prefix quotes/brackets and currency symbols belong to the next word.
        if (not result or text[0] in _OPENING_SYMBOLS) and index + 1 < len(words):
            pending_prefix.append(current)
        elif result:
            previous = result.pop()
            result.append(_merge_word_parts(
                [previous, current], previous["text"] + text,
            ))
        elif index + 1 < len(words):
            pending_prefix.append(current)
        # A symbol-only OCR row has no clickable word target, so it is omitted.
        index += 1

    if pending_prefix and result:
        previous = result.pop()
        result.append(_merge_word_parts(
            [previous, *pending_prefix],
            previous["text"] + "".join(clean_ocr_text(part.get("text", "")) for part in pending_prefix),
        ))
    return result


def _word_height(word: dict) -> float:
    box = word.get("box", [])
    return float(box[3]) if isinstance(box, list) and len(box) == 4 else 0.0


def is_sentence_boundary(word: str, previous: str = "", following: str = "") -> bool:
    """Return whether a word closes a reading sentence.

    This runs on OCR tokens rather than the whole visual line, so a sentence
    ending before another token on the same line becomes two logical lines
    without losing original word boxes. Dots in common abbreviations, initials
    and decimal numbers remain within the same sentence.
    """
    value = clean_ocr_text(word)
    if _HARD_SENTENCE_END.search(value):
        return True
    if not _DOT_SENTENCE_END.search(value):
        return False
    plain = re.sub(r"[\"')\]\u201d\u2019.]+$", "", value).lower()
    previous_plain = re.sub(r"[^A-Za-z]", "", clean_ocr_text(previous)).lower()
    following_letters = re.sub(r"[^A-Za-z]", "", clean_ocr_text(following))
    # Paddle can return full stops as their own word. Keep ``Dr . Brown`` and
    # initials together, but treat a stand-alone stop after normal prose as a
    # sentence boundary.
    if value.startswith(".") and not plain:
        if previous_plain in _COMMON_ABBREVIATIONS:
            return False
        if len(previous_plain) == 1 and following_letters[:1].isupper():
            return False
        return bool(previous_plain)
    if plain in _COMMON_ABBREVIATIONS:
        return False
    if re.fullmatch(r"(?:[a-z]\.)+[a-z]?", value.lower()):
        return False
    if re.fullmatch(r"\d+(?:\.\d+)+", plain):
        return False
    return bool(re.search(r"[A-Za-z]", plain))


def split_row_at_sentence_boundaries(row: dict) -> list[dict]:
    """Split one OCR visual row into sentence-aware logical rows."""
    words = row.get("words", [])
    if not words:
        return []
    parts: list[dict] = []
    current: list[dict] = []
    for index, word in enumerate(words):
        current.append(word)
        previous = words[index - 1]["text"] if index else ""
        following = words[index + 1]["text"] if index + 1 < len(words) else ""
        if is_sentence_boundary(word["text"], previous, following):
            parts.append({
                "text": join_ocr_words(current),
                "words": current,
                "box": row_box({}, current),
                "confidence": row.get("confidence"),
                "needs_review": bool(row.get("needs_review")),
                "hard_break_after": True,
                "continuation_from_sentence_split": bool(parts),
            })
            current = []
    if current:
        parts.append({
            "text": join_ocr_words(current),
            "words": current,
            "box": row_box({}, current),
            "confidence": row.get("confidence"),
            "needs_review": bool(row.get("needs_review")),
            "hard_break_after": False,
            "continuation_from_sentence_split": bool(parts),
        })
    return parts


def preserve_independent_layout(text: str, words: list[dict], median_height: float = 0.0) -> bool:
    """Keep headings, lists, tables and blank-answer rows out of prose joins."""
    if _BULLET_OR_ITEM.match(text) or "|" in text or re.search(r"_{2,}", text):
        return True
    # Multiple large horizontal gaps are usually vocabulary/table columns.
    ordered = sorted(words, key=lambda word: word["box"][0])
    gaps = [
        ordered[index + 1]["box"][0] - (ordered[index]["box"][0] + ordered[index]["box"][2])
        for index in range(len(ordered) - 1)
    ]
    if sum(gap > 0.045 for gap in gaps) >= 2:
        return True
    # Short all-caps labels such as "UNIT 1" are titles, not sentence wraps.
    letters = re.sub(r"[^A-Za-z]", "", text)
    if letters and letters.isupper() and len(letters) <= 24:
        return True
    # Textbook section labels are often title case rather than all caps. Keep
    # a short, visibly larger line separate from the nearby dialogue body.
    row_height = max((_word_height(word) for word in words), default=0.0)
    short_label = 0 < len(words) <= 5 and not _SENTENCE_END.search(text)
    return bool(short_label and median_height > 0 and row_height >= median_height * 1.22)


def should_join(previous: dict, current: dict) -> bool:
    if previous["independent"] or current["independent"]:
        return False
    if previous.get("hard_break_after"):
        return False
    if _SENTENCE_END.search(previous["text"]):
        return False
    prior, following = previous.get("box"), current.get("box")
    if not prior or not following:
        return True
    # Same-column, nearby visual lines are one reading paragraph. This avoids
    # crossing into a second column or a distant title/caption.
    # A sentence that begins after punctuation near the right side of a line
    # may wrap back to the left edge on the next visual row: ``...! My / father``.
    # Preserve that continuation without relaxing ordinary two-column joins.
    wrapped_back_to_left = (
        bool(previous.get("continuation_from_sentence_split"))
        and following[0] < prior[0]
        and prior[0] - following[0] <= 0.32
    )
    same_column = abs(prior[0] - following[0]) <= 0.12 or wrapped_back_to_left
    vertical_gap = following[1] - (prior[1] + prior[3])
    return same_column and -0.02 <= vertical_gap <= max(0.055, prior[3] * 3)


def continuation_score(previous: dict, current: dict, order_distance: int) -> float | None:
    """Rank a possible wrapped-line continuation by page geometry.

    OCR engines commonly interleave two nearby speech bubbles or columns in
    their row order. A continuation therefore cannot be limited to the next
    returned row. Prefer the closest lower row with matching horizontal
    alignment, while the existing sentence/layout guards in ``should_join``
    prevent crossing punctuation, headings, lists, tables and blanks.
    """
    if not should_join(previous, current):
        return None
    prior, following = previous.get("box"), current.get("box")
    if not prior or not following:
        return float(order_distance)
    if following[1] < prior[1] - 0.005:
        return None
    prior_right = prior[0] + prior[2]
    following_right = following[0] + following[2]
    overlap = min(prior_right, following_right) - max(prior[0], following[0])
    wrapped_back_to_left = (
        bool(previous.get("continuation_from_sentence_split"))
        and following[0] < prior[0]
    )
    if overlap <= 0 and not wrapped_back_to_left:
        return None
    vertical_gap = max(0.0, following[1] - (prior[1] + prior[3]))
    left_alignment = abs(prior[0] - following[0])
    if wrapped_back_to_left:
        # A sentence tail at the right edge wrapping to the next line's left
        # edge is expected; vertical proximity is more meaningful here.
        left_alignment = min(left_alignment, 0.03)
    return vertical_gap * 4 + left_alignment + order_distance * 0.0001


def is_reliable_single_word_segment(text: str, words: list[dict]) -> bool:
    """Return whether a new OCR segment should generate word audio only."""
    if len(words) != 1 or bool(words[0].get("ocr_needs_review")):
        return False
    word_text = clean_ocr_text(words[0].get("text", ""))
    segment_text = clean_ocr_text(text)
    if not word_text or re.search(r"\s", word_text):
        return False
    word_key = re.sub(r"[^A-Za-z0-9]", "", word_text).lower()
    segment_key = re.sub(r"[^A-Za-z0-9]", "", segment_text).lower()
    return bool(word_key and word_key == segment_key and len(word_key) <= 40)


def candidate_segments(rows: list[dict], page: int) -> list[dict]:
    """Create paragraph-aware review candidates with normalized word boxes."""
    prepared = []
    for row in rows:
        line_text = clean_ocr_text(row.get("text", ""))
        # Decorative marks and very-low-confidence single glyphs are noise.
        confidence = row.get("confidence")
        if not re.search(r"[A-Za-z]", line_text):
            continue
        if isinstance(confidence, (int, float)) and confidence < 0.55 and len(re.sub(r"[^A-Za-z]", "", line_text)) < 3:
            continue
        words = []
        for source in row.get("words", []):
            text = clean_ocr_text(source.get("text", ""))
            box = source.get("box")
            if not text or not isinstance(box, list) or len(box) != 4:
                continue
            word = {"text": text, "box": [round(float(value), 6) for value in box]}
            if "confidence" in source:
                word["ocr_confidence"] = source["confidence"]
                word["ocr_needs_review"] = bool(source.get("needs_review"))
            words.append(word)
        if words:
            words = merge_detached_symbols(words)
        if words:
            prepared.extend(split_row_at_sentence_boundaries({
                "words": words, "box": row_box(row, words),
                "confidence": row.get("confidence"), "needs_review": row.get("needs_review"),
            }))

    line_heights = [max((_word_height(word) for word in row["words"]), default=0.0)
                    for row in prepared]
    line_heights = [height for height in line_heights if height > 0]
    median_height = statistics.median(line_heights) if line_heights else 0.0
    for row in prepared:
        row["independent"] = preserve_independent_layout(row["text"], row["words"], median_height)

    segments = []
    groups: list[list[dict]] = []
    consumed: set[int] = set()
    for index, row in enumerate(prepared):
        if index in consumed:
            continue
        group: list[dict] = []
        current_index = index
        while current_index not in consumed:
            current = prepared[current_index]
            group.append(current)
            consumed.add(current_index)
            if current.get("hard_break_after") or current.get("independent"):
                break
            choices: list[tuple[float, int]] = []
            for candidate_index in range(current_index + 1, len(prepared)):
                if candidate_index in consumed:
                    continue
                score = continuation_score(
                    current, prepared[candidate_index], candidate_index - current_index
                )
                if score is not None:
                    choices.append((score, candidate_index))
            if not choices:
                break
            _, current_index = min(choices)
        groups.append(group)
    for group in groups:
        segment_id = f"p{page}-s{len(segments)}"
        words = []
        for line in group:
            words.extend(line["words"])
        for index, word in enumerate(words):
            word["id"] = f"{segment_id}-w{index}"
            word["meaning"] = ""
        anchor = row_box({}, words)
        segment = {"id": segment_id, "label": "待校对", "text": join_ocr_words(words),
                         "translation": "", "anchor": [round(value, 6) for value in anchor] if anchor else None,
                         "words": words}
        if is_reliable_single_word_segment(segment["text"], words):
            segment["audio_mode"] = "word_only"
        confidences = [float(line["confidence"]) for line in group if isinstance(line.get("confidence"), (int, float))]
        if confidences:
            segment["ocr_confidence"] = round(min(confidences), 6)
            segment["ocr_needs_review"] = any(bool(line["needs_review"]) for line in group)
        segments.append(segment)
    return segments


def convert_page(args: argparse.Namespace, total: int, page: int,
                 local_provider: LocalPaddleOCRProvider | None,
                 model_id: str = "local-paddleocr") -> tuple[LocalPaddleOCRProvider | None, dict]:
    """Render and OCR one page; the daemon reuses the returned engine."""
    dirs = ensure_book_tree(args.book_id)
    shutil.copy2(args.input, dirs["source"] / "original.pdf")
    stem = page_stem(page)
    image, webp_image = render_page_images(args.input, page, stem, dirs)
    try:
        rows = text_layer(args.input, page)
    except (subprocess.CalledProcessError, ET.ParseError):
        rows = []
    reliable_text = text_layer_is_reliable(rows)
    method = "pdf-text"
    request_id = ""
    word_box_quality: dict[str, object] = {}
    if not args.skip_ocr and model_id == "qwen3.5-ocr":
        # Qwen3.5 keeps its own text result, while this existing warm provider
        # is used only for word geometry.  The local OCR branch below is
        # unchanged and continues to use the same provider normally.
        if local_provider is None:
            print(json.dumps({"page": page - 1, "progress": page - 1, "total": total,
                              "stage": "loading-paddleocr-word-boxes"}), flush=True)
            local_provider = LocalPaddleOCRProvider()
        result = create_ocr_provider(model_id, local_provider).recognize(image)
        rows, method, request_id = result.rows, result.method, result.request_id
        word_box_quality = result.quality
    elif not args.skip_ocr and not reliable_text:
        if local_provider is None:
            print(json.dumps({"page": page - 1, "progress": page - 1, "total": total,
                              "stage": "loading-paddleocr"}), flush=True)
            local_provider = LocalPaddleOCRProvider()
        result = local_provider.recognize(image)
        rows, method = result.rows, result.method
    quality = {"pdf_text_reliable": reliable_text,
               "review_word_count": sum(bool(word.get("needs_review"))
                                        for row in rows for word in row.get("words", []))}
    quality.update(word_box_quality)
    (dirs["ocr"] / f"{stem}.json").write_text(
        json.dumps({"method": method, "model": model_id, "provider": "dashscope" if model_id == "qwen3.5-ocr" else "local",
                    "request_id": request_id, "quality": quality, "rows": rows}, ensure_ascii=False, indent=2) + "\n")
    segments = candidate_segments(rows, page)
    content_relative = f"metadata/pages/{stem}.json"
    content = {"book_id": args.book_id, "page": page, "segments": segments,
               "reviewed": False, "source": {"ocr": f"ocr/{stem}.json"}}
    (dirs["metadata"] / "pages" / f"{stem}.json").write_text(
        json.dumps(content, ensure_ascii=False, indent=2) + "\n")
    image.unlink(missing_ok=True)
    page_entry = {"page": page, "printed_page": None, "page_group": "", "page_label": "",
                  "title": "", "unit": "", "image": f"pages/{webp_image.name}",
                  "content": content_relative,
                  "interactive": bool(segments)}
    manifest = {"schema_version": 1, "source_page_count": total,
                "book": {"book_id": args.book_id, "title": args.title,
                "subtitle": args.subtitle, "description": args.description,
                "publisher": args.publisher, "grade": args.grade,
                "semester": args.semester, "cover": page_entry["image"],
                "sort": args.sort}, "pages": [page_entry]}
    (dirs["metadata"] / "book.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    return local_provider, {"page": page, "progress": page, "total": total,
                            "request_id": request_id}


def daemon() -> None:
    """Serve page OCR requests over stdin, keeping PaddleOCR warm."""
    local_provider: LocalPaddleOCRProvider | None = None
    for line in sys.stdin:
        try:
            request = json.loads(line)
            resource_root = request.get("resource_root")
            if not isinstance(resource_root, str) or not resource_root.strip():
                raise ValueError("OCR request is missing resource_root")
            # The daemon is intentionally long-lived so PaddleOCR stays warm,
            # but its output root belongs to the current draft. Update the
            # per-request root before ensure_book_tree() resolves any paths.
            resource_paths.RESOURCE_ROOT = Path(resource_root).expanduser().resolve()
            args = argparse.Namespace(
                input=Path(request["input"]), book_id=request["book_id"],
                title=request["title"], subtitle=request.get("subtitle", ""),
                description=request.get("description", ""), publisher=request.get("publisher", ""),
                grade=request.get("grade", ""), semester=request.get("semester", ""),
                sort=int(request.get("sort", 0)), skip_ocr=bool(request.get("skip_ocr", False)))
            validate_book_id(args.book_id)
            info = run("pdfinfo", str(args.input)).decode(errors="replace")
            match = re.search(r"^Pages:\s+(\d+)", info, re.MULTILINE)
            total = int(match[1]) if match else 0
            page = int(request["page"])
            if not args.input.is_file() or not 1 <= page <= total:
                raise ValueError("invalid OCR page request")
            model_id = str(request.get("model_id") or "local-paddleocr")
            local_provider, event = convert_page(args, total, page, local_provider, model_id)
            print(json.dumps({**event, "done": True}), flush=True)
        except Exception as exc:  # keep the warm worker alive for the next page
            print(json.dumps({"error": str(exc), "done": True}), flush=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--book-id", required=True)
    parser.add_argument("--title", required=True)
    parser.add_argument("--subtitle", default="")
    parser.add_argument("--description", default="")
    parser.add_argument("--publisher", default="")
    parser.add_argument("--grade", default="")
    parser.add_argument("--semester", default="")
    parser.add_argument("--sort", type=int, default=0)
    parser.add_argument("--page", type=int,
                        help="convert exactly this PDF page; defaults to all pages for standalone use")
    parser.add_argument("--skip-ocr", action="store_true")
    parser.add_argument("--ocr-model", choices=("local-paddleocr", "qwen3.5-ocr"),
                        default="local-paddleocr")
    args = parser.parse_args()
    validate_book_id(args.book_id)
    if not args.input.is_file():
        parser.error("--input must be an existing PDF")
    for command in ("pdfinfo", "pdftoppm", "cwebp"):
        if not shutil.which(command):
            parser.error(f"required command is missing: {command}")
    info = run("pdfinfo", str(args.input)).decode(errors="replace")
    match = re.search(r"^Pages:\s+(\d+)", info, re.MULTILINE)
    if not match or not 1 <= int(match[1]) <= 200:
        parser.error("PDF must contain 1-200 pages")
    if re.search(r"^Encrypted:\s+yes", info, re.MULTILINE):
        parser.error("password-protected PDFs are not supported")
    total = int(match[1])
    if args.page is not None and not 1 <= args.page <= total:
        parser.error(f"--page must be between 1 and {total}")
    print(json.dumps({"page": 0, "progress": 0, "total": total}), flush=True)
    dirs = ensure_book_tree(args.book_id)
    shutil.copy2(args.input, dirs["source"] / "original.pdf")
    paddle: PaddleOCREngine | None = None
    qwen_word_locator: LocalPaddleOCRProvider | None = None
    pages = []
    page_numbers = [args.page] if args.page is not None else range(1, total + 1)
    for page in page_numbers:
        stem = page_stem(page)
        image, webp_image = render_page_images(args.input, page, stem, dirs)
        try:
            rows = text_layer(args.input, page)
        except (subprocess.CalledProcessError, ET.ParseError):
            rows = []
        reliable_text = text_layer_is_reliable(rows)
        method = "pdf-text"
        request_id = ""
        word_box_quality: dict[str, object] = {}
        if not args.skip_ocr and args.ocr_model == "qwen3.5-ocr":
            if qwen_word_locator is None:
                print(json.dumps({"page": page - 1, "progress": page - 1, "total": total,
                                  "stage": "loading-paddleocr-word-boxes"}), flush=True)
                qwen_word_locator = LocalPaddleOCRProvider()
            result = create_ocr_provider(args.ocr_model, qwen_word_locator).recognize(image)
            rows, method, request_id = result.rows, result.method, result.request_id
            word_box_quality = result.quality
        elif not args.skip_ocr and not reliable_text:
            if paddle is None:
                print(json.dumps({"page": page - 1, "progress": page - 1, "total": total,
                                  "stage": "loading-paddleocr"}), flush=True)
                paddle = PaddleOCREngine()
            rows, method = paddle.rows(image), "paddleocr-ppocrv5"
        image.unlink(missing_ok=True)
        quality = {"pdf_text_reliable": reliable_text,
                   "review_word_count": sum(bool(word.get("needs_review"))
                                            for row in rows for word in row.get("words", []))}
        quality.update(word_box_quality)
        (dirs["ocr"] / f"{stem}.json").write_text(json.dumps({
            "method": method, "model": args.ocr_model,
            "provider": "dashscope" if args.ocr_model == "qwen3.5-ocr" else "local",
            "request_id": request_id, "quality": quality, "rows": rows,
        }, ensure_ascii=False, indent=2) + "\n")
        segments = candidate_segments(rows, page)
        content_relative = f"metadata/pages/{stem}.json"
        content = {"book_id": args.book_id, "page": page, "segments": segments,
                   "reviewed": False, "source": {"ocr": f"ocr/{stem}.json"}}
        (dirs["metadata"] / "pages" / f"{stem}.json").write_text(json.dumps(content, ensure_ascii=False, indent=2) + "\n")
        pages.append({"page": page, "printed_page": None, "page_group": "", "page_label": "",
                      "title": "", "unit": "", "image": f"pages/{webp_image.name}",
                      "content": content_relative,
                      "interactive": bool(segments)})
        print(json.dumps({"page": page, "progress": page, "total": total}), flush=True)
    manifest = {"schema_version": 1, "source_page_count": total,
                "book": {"book_id": args.book_id, "title": args.title,
                "subtitle": args.subtitle, "description": args.description, "publisher": args.publisher,
                "grade": args.grade, "semester": args.semester, "cover": pages[0]["image"], "sort": args.sort},
                "pages": pages}
    (dirs["metadata"] / "book.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")


if __name__ == "__main__":
    try:
        if "--daemon" in sys.argv:
            daemon()
            raise SystemExit(0)
        main()
    except subprocess.CalledProcessError as exc:
        print(exc.stderr.decode(errors="replace"), file=sys.stderr)
        raise
