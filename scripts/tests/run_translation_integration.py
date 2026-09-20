#!/usr/bin/env python3
"""Live one-page Translator + Reviewer acceptance checks."""
from __future__ import annotations

from translate_page import configured_backend, translate_page


def contains_any(value: str, expected: tuple[str, ...]) -> bool:
    return any(item in value for item in expected)


def main() -> None:
    backend = configured_backend()
    sentence_cases = [
        ("Doctors are great! My father is a doctor too!", ("医生",)),
        ("He plays football.", ("踢足球",)),
        ("We watched a play yesterday.", ("戏剧", "演出", "戏")),
        ("I went to the bank to get some money.", ("银行",)),
        ("We sat on the river bank.", ("河岸", "岸边", "河边")),
        ("I read books every day.", ("读", "看书")),
        ("Yesterday I read this book.", ("昨天", "读", "看")),
        ("He doesn't like football.", ("不喜欢", "不爱")),
        ("Hello!", ("你好",)),
        ("Yes.", ("是", "对")),
        ("No.", ("不", "没有")),
        ("Thank you.", ("谢谢",)),
        ("Tom is my friend.", ("汤姆",)),
    ]
    word_cases = {
        1: [("play", ("踢", "球"))],
        2: [("play", ("戏剧", "演出", "戏"))],
        3: [("bank", ("银行",))],
        4: [("bank", ("河岸", "岸边", "河边"))],
        5: [("read", ("读", "看书"))],
        6: [("read", ("读", "看"))],
    }
    segments = []
    for index, (text, _) in enumerate(sentence_cases):
        words = [
            {"id": f"s{index}-w{word_index}", "text": word}
            for word_index, (word, _) in enumerate(word_cases.get(index, []))
        ]
        segments.append(
            {
                "id": f"s{index}",
                "text": text,
                "translation": "old translation",
                "words": words,
            }
        )
    result = translate_page({"page": 1, "segments": segments}, backend)
    failures: list[str] = []
    for index, (_, expected) in enumerate(sentence_cases):
        translation = result["segments"][index]["translation"]
        print(f"PASS? sentence={sentence_cases[index][0]!r} translation={translation!r}")
        if not contains_any(translation, expected):
            failures.append(f"sentence {index} missing {expected}: {translation!r}")
        for word_index, (word, word_expected) in enumerate(word_cases.get(index, [])):
            meaning = result["segments"][index]["words"][word_index]["meaning"]
            print(f"PASS? word={word!r} meaning={meaning!r}")
            if not contains_any(meaning, word_expected):
                failures.append(f"word {word!r} missing {word_expected}: {meaning!r}")
    if failures:
        raise SystemExit("\n".join(failures))
    print("All live one-page translation and reviewer checks passed.")


if __name__ == "__main__":
    main()
