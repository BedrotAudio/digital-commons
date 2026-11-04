"""Keyword retrieval over document sections. Groups filter before any ranking."""

from __future__ import annotations

import logging
import math
import re
from collections import Counter
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

import yaml

from app.models.domain import Passage

logger = logging.getLogger("digital_commons.retriever")

_WORD = re.compile(r"[a-z0-9]+")
_FRONT_MATTER = re.compile(r"\A---\s*\n(.*?)\n---\s*\n", re.DOTALL)
_SECTION = re.compile(r"^## +(.+?)\s*$", re.MULTILINE)
_STOP_WORDS = frozenset(
    "a an and are as at be by can do does for from has have how i if in is it may me my of "
    "on or our should that the their this to was we what when where which who will with you".split()
)
_SUFFIXES = ("ibility", "ability", "ations", "ation", "ible", "able", "ing", "ies", "ed", "es", "s", "y")
_MIN_STEM = 3
_SNIPPET_CHARS = 200
K1 = 1.5
B = 0.75


def _stem(word: str) -> str:
    """Enough to match eligible with eligibility, and policy with policies."""
    for suffix in _SUFFIXES:
        if word.endswith(suffix) and len(word) - len(suffix) >= _MIN_STEM:
            return word[: -len(suffix)]
    return word


def tokenize(text: str) -> list[str]:
    return [_stem(word) for word in _WORD.findall(text.lower()) if word not in _STOP_WORDS]


class Retriever(Protocol):
    def search(self, query: str, groups: Sequence[str], limit: int) -> list[Passage]: ...

    def get_section(
        self, groups: Sequence[str], document_id: str, section: str
    ) -> Passage | None: ...


@dataclass
class _Section:
    passage: Passage
    allowed_groups: frozenset[str]
    terms: Counter[str]
    length: int


def _snippet(text: str) -> str:
    flat = " ".join(text.split())
    if len(flat) <= _SNIPPET_CHARS:
        return flat
    return flat[:_SNIPPET_CHARS].rsplit(" ", 1)[0] + "..."


def _parse_document(path: Path) -> list[_Section]:
    source = path.read_text(encoding="utf-8")
    match = _FRONT_MATTER.match(source)
    if not match:
        raise ValueError("no front matter")
    meta = yaml.safe_load(match.group(1))
    body = source[match.end() :]
    headings = list(_SECTION.finditer(body))
    sections = []
    for index, heading in enumerate(headings):
        end = headings[index + 1].start() if index + 1 < len(headings) else len(body)
        text = body[heading.end() : end].strip()
        name = heading.group(1)
        passage = Passage(
            document_id=str(meta["document_id"]),
            title=str(meta["title"]),
            department=str(meta["department"]),
            classification=str(meta["classification"]),
            version=str(meta["version"]),
            document_date=str(meta["document_date"]),
            source_filename=str(meta.get("source_filename") or path.name),
            section=name,
            text=text,
            snippet=_snippet(text),
            sample=meta.get("sample") is True,
        )
        words = tokenize(f"{meta['title']} {name} {text}")
        sections.append(
            _Section(
                passage=passage,
                allowed_groups=frozenset(str(item) for item in meta.get("allowed_groups") or []),
                terms=Counter(words),
                length=len(words),
            )
        )
    return sections


class KeywordRetriever:
    """A small BM25. Statistics use only sections the caller may see."""

    def __init__(self, folder: Path) -> None:
        self._sections: list[_Section] = []
        if not folder.is_dir():
            return
        for path in sorted(folder.glob("*.md")):
            try:
                self._sections.extend(_parse_document(path))
            except (ValueError, KeyError, TypeError, yaml.YAMLError) as error:
                logger.warning("skipped document file=%s error=%s", path.name, type(error).__name__)

    def _visible(self, groups: Sequence[str]) -> list[_Section]:
        permitted = set(groups)
        return [section for section in self._sections if section.allowed_groups & permitted]

    def search(self, query: str, groups: Sequence[str], limit: int) -> list[Passage]:
        candidates = self._visible(groups)
        terms = set(tokenize(query))
        if not candidates or not terms:
            return []
        average = sum(section.length for section in candidates) / len(candidates) or 1.0
        frequency = {
            term: sum(term in section.terms for section in candidates) for term in terms
        }
        scored = []
        for order, section in enumerate(candidates):
            score = 0.0
            for term in terms:
                count = section.terms[term]
                if not count:
                    continue
                rarity = math.log(
                    1 + (len(candidates) - frequency[term] + 0.5) / (frequency[term] + 0.5)
                )
                norm = K1 * (1 - B + B * section.length / average)
                score += rarity * count * (K1 + 1) / (count + norm)
            if score > 0:
                scored.append((-score, order, section))
        scored.sort(key=lambda item: item[:2])
        return [section.passage for _, _, section in scored[:limit]]

    def get_section(
        self, groups: Sequence[str], document_id: str, section: str
    ) -> Passage | None:
        for candidate in self._visible(groups):
            passage = candidate.passage
            if passage.document_id == document_id and passage.section == section:
                return passage
        return None


def citation_wire(number: int, passage: Passage, version: str | None = None) -> dict:
    return {
        "number": number,
        "document_id": passage.document_id,
        "title": passage.title,
        "department": passage.department,
        "classification": passage.classification,
        "version": version if version is not None else passage.version,
        "document_date": passage.document_date,
        "source_filename": passage.source_filename,
        "section": passage.section,
        "page": passage.page,
        "snippet": passage.snippet,
        "sample": passage.sample,
    }
