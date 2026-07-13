#!/usr/bin/env python3
"""Validate repository-local Markdown links, anchors, and public terminology."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[2]
SKIP_PARTS = {".git", ".worktrees", "node_modules"}
HISTORICAL_TERMS = re.compile(r"\b(?:ccflow|muxwatch)\b", re.IGNORECASE)
LINK = re.compile(r"!?\[[^\]]*\]\(([^)]+)\)")
HEADING = re.compile(r"^#{1,6}\s+(.+?)\s*#*\s*$", re.MULTILINE)


def included(path: Path) -> bool:
    rel = path.relative_to(ROOT)
    if any(part in SKIP_PARTS for part in rel.parts):
        return False
    return not (len(rel.parts) >= 2 and rel.parts[:2] == (".claude", "worktrees"))


def markdown_files() -> list[Path]:
    return sorted(path for path in ROOT.rglob("*.md") if included(path))


def slug(text: str) -> str:
    text = re.sub(r"<[^>]+>", "", text.lower())
    text = re.sub(r"[`*_~]", "", text)
    text = re.sub(r"[^\w\- ]", "", text)
    return re.sub(r"\s", "-", text).strip("-")


def anchors(path: Path) -> set[str]:
    found: set[str] = set()
    counts: dict[str, int] = {}
    for heading in HEADING.findall(path.read_text(encoding="utf-8")):
        base = slug(heading)
        count = counts.get(base, 0)
        counts[base] = count + 1
        found.add(base if count == 0 else f"{base}-{count}")
    return found


def split_target(raw: str) -> tuple[str, str]:
    raw = raw.strip()
    if raw.startswith("<") and ">" in raw:
        raw = raw[1 : raw.index(">")]
    else:
        raw = raw.split(maxsplit=1)[0]
    path, marker, fragment = unquote(raw).partition("#")
    return path, fragment if marker else ""


def check_links(files: list[Path]) -> list[str]:
    failures: list[str] = []
    anchor_cache: dict[Path, set[str]] = {}
    for source in files:
        for raw in LINK.findall(source.read_text(encoding="utf-8")):
            target_text, fragment = split_target(raw)
            if target_text.lower() in {"url", "path", "target"}:
                continue  # Markdown-syntax placeholder example.
            if re.match(r"^(?:[a-z][a-z0-9+.-]*:|//)", target_text, re.I):
                continue
            if any(token in target_text for token in ("<", ">", "{", "}")):
                continue  # Explicit placeholder example.
            target = (source.parent / target_text).resolve() if target_text else source
            try:
                target.relative_to(ROOT)
            except ValueError:
                failures.append(f"{source.relative_to(ROOT)}: link escapes repository: {raw}")
                continue
            if not target.exists():
                failures.append(f"{source.relative_to(ROOT)}: missing local target: {raw}")
                continue
            if fragment and target.is_file() and target.suffix.lower() == ".md":
                available = anchor_cache.setdefault(target, anchors(target))
                if fragment.lower() not in available:
                    failures.append(f"{source.relative_to(ROOT)}: missing anchor #{fragment} in {target.relative_to(ROOT)}")
    return failures


def check_terms(files: list[Path]) -> list[str]:
    failures: list[str] = []
    exempt_file = ROOT / "docs/cohesive-package.md"
    for path in files:
        if path == exempt_file:
            continue  # Completed historical migration record.
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not HISTORICAL_TERMS.search(line):
                continue
            if re.search(r"\b(?:legacy|migrat(?:e|ed|ion))\b", line, re.I):
                continue  # Historical migration note, not public naming.
            failures.append(f"{path.relative_to(ROOT)}:{number}: retired public terminology: {line.strip()}")
    return failures


def main() -> int:
    files = markdown_files()
    failures = check_links(files) + check_terms(files)
    if failures:
        print("documentation consistency check failed:", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1
    print(f"documentation consistency check passed ({len(files)} Markdown files)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
