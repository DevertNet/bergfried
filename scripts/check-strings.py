#!/usr/bin/env python3
"""Checks the app's German localisation.

* every key used with L("…") / LT("…") in app/Sources must exist in app/Resources/de.lproj/Localizable.strings,
  unless it is listed in app/Resources/same-in-german.txt (one key per line: text that is identical in German);
* the key and its German value must contain the same format specifiers (%@, %lld, …) in the same order;
* the .strings file must have no duplicate keys.
Exit status 1 on any problem (used by CI).
"""
import glob, re, sys

ROOT = __file__.rsplit("/scripts/", 1)[0] if "/scripts/" in __file__ else "."
SRC = glob.glob(ROOT + "/app/Sources/*.swift")
STRINGS = ROOT + "/app/Resources/de.lproj/Localizable.strings"
SAME = ROOT + "/app/Resources/same-in-german.txt"
SPEC = re.compile(r"%(?:\d+\$)?(?:ll?[dDuU]|@|[dDuUfFsS])")


def unescape(s):
    return s.replace('\\"', '"').replace("\\n", "\n").replace("\\\\", "\\")


def parse_strings(text):
    pairs, dups = {}, []
    for m in re.finditer(r'^"((?:[^"\\]|\\.)*)"\s*=\s*"((?:[^"\\]|\\.)*)"\s*;', text, re.M):
        k, v = unescape(m.group(1)), unescape(m.group(2))
        if k in pairs:
            dups.append(k)
        pairs[k] = v
    return pairs, dups


def main():
    pairs, dups = parse_strings(open(STRINGS, encoding="utf-8").read())
    try:
        same = {l.rstrip("\n") for l in open(SAME, encoding="utf-8") if l.strip() and not l.startswith("#")}
    except FileNotFoundError:
        same = set()
    problems = [f"duplicate key in Localizable.strings: {k!r}" for k in dups]
    used = {}
    for f in SRC:
        for m in re.finditer(r'\bLT?\(\s*"((?:[^"\\]|\\.)*)"', open(f, encoding="utf-8").read()):
            used.setdefault(unescape(m.group(1)), f)
    for k, f in sorted(used.items()):
        if k in pairs:
            if SPEC.findall(k) != SPEC.findall(pairs[k]):
                problems.append(f"format specifiers differ for {k!r}: {SPEC.findall(k)} vs {SPEC.findall(pairs[k])}")
        elif k not in same:
            problems.append(f"missing German translation for {k!r} ({f.split('/')[-1]})")
    for k in sorted(same):
        if k in pairs:
            problems.append(f"{k!r} is in same-in-german.txt but also translated")
    for p in problems:
        print("ERROR:", p)
    print(f"{len(used)} keys used, {len(pairs)} translations, {len(same)} marked identical, {len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
