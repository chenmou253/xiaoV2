"""The single Python-side definition of the xiaoV2 book resource tree."""
from __future__ import annotations

import os
import re
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[2]
RESOURCE_ROOT = Path(os.environ.get("RESOURCE_ROOT", PROJECT_ROOT / "storage/books")).expanduser().resolve()
BOOK_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$")
KINDS = ("source", "pages", "audio", "tts", "ocr", "text", "metadata", "cache")


def validate_book_id(book_id: str) -> str:
    if not BOOK_ID_PATTERN.fullmatch(book_id):
        raise ValueError("invalid book_id: use 1-80 URL-safe letters, digits, '-' or '_'")
    return book_id


def _contained(root: Path, target: Path) -> Path:
    resolved = target.resolve()
    try:
        resolved.relative_to(root.resolve())
    except ValueError as exc:
        raise ValueError("resource path escapes its allowed root") from exc
    return resolved


def book_root(book_id: str) -> Path:
    return _contained(RESOURCE_ROOT, RESOURCE_ROOT / validate_book_id(book_id))


def resource_dir(book_id: str, kind: str) -> Path:
    if kind not in KINDS:
        raise ValueError(f"unknown resource directory: {kind}")
    return _contained(book_root(book_id), book_root(book_id) / kind)


def relative_path(book_id: str, value: str | Path) -> Path:
    value = Path(value)
    if value.is_absolute():
        raise ValueError("resource path must be relative")
    return _contained(book_root(book_id), book_root(book_id) / value)


def page_stem(page: int) -> str:
    if not 1 <= page <= 999:
        raise ValueError("page must be between 1 and 999")
    return f"page-{page:03d}"


def ensure_book_tree(book_id: str) -> dict[str, Path]:
    directories = {kind: resource_dir(book_id, kind) for kind in KINDS}
    for directory in directories.values():
        directory.mkdir(parents=True, exist_ok=True)
    (directories["metadata"] / "pages").mkdir(parents=True, exist_ok=True)
    return directories
