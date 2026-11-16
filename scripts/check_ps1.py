#!/usr/bin/env python3
"""Статическая проверка .ps1 без PowerShell (на Linux его нет): баланс (), {}, [], закрытые строки и here-строки.

Это не разбор языка, а страховка от обрезанного или битого файла; настоящий разбор
([System.Management.Automation.Language.Parser]::ParseFile) идёт в scripts/check-ps1.sh, когда есть pwsh/powershell.
    check_ps1.py файл.ps1 ...
"""
import sys

PAIRS = {")": "(", "}": "{", "]": "["}


def check(path: str) -> list[str]:
    text = open(path, encoding="utf-8-sig").read()
    errs: list[str] = []
    stack: list[tuple[str, int]] = []
    i, n, line = 0, len(text), 1
    while i < n:
        c = text[i]
        if c == "\n":
            line += 1
            i += 1
        elif c == "`":  # экранирование
            i += 2
        elif c == "#":  # комментарий до конца строки
            while i < n and text[i] != "\n":
                i += 1
        elif c == "<" and text.startswith("<#", i):  # блочный комментарий
            j = text.find("#>", i + 2)
            if j < 0:
                errs.append(f"{path}:{line}: не закрыт блочный комментарий <#")
                break
            line += text.count("\n", i, j)
            i = j + 2
        elif c == "@" and i + 1 < n and text[i + 1] in "\"'" and text[i + 2 : i + 3] in ("\n", "\r"):  # here-строка
            q = text[i + 1]
            start_line = line
            j = text.find("\n" + q + "@", i)
            if j < 0:
                errs.append(f"{path}:{start_line}: не закрыта here-строка @{q}")
                break
            line += text.count("\n", i, j + 1)
            i = j + 3
        elif c in "\"'":
            q, start_line = c, line
            i += 1
            while i < n:
                if text[i] == "`" and q == '"':
                    i += 2
                    continue
                if text[i] == "\n":
                    line += 1  # строки в PowerShell бывают многострочными
                if text[i] == q:
                    if q == "'" and text[i + 1 : i + 2] == "'":  # '' внутри '...'
                        i += 2
                        continue
                    break
                i += 1
            else:
                errs.append(f"{path}:{start_line}: не закрыта строка {q}")
                break
            i += 1
        elif c in "({[":
            stack.append((c, line))
            i += 1
        elif c in PAIRS:
            if not stack or stack[-1][0] != PAIRS[c]:
                errs.append(f"{path}:{line}: лишняя '{c}'")
                break
            stack.pop()
            i += 1
        else:
            i += 1
    else:
        for c, ln in stack:
            errs.append(f"{path}:{ln}: не закрыта '{c}'")
    return errs


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    bad = [e for p in sys.argv[1:] for e in check(p)]
    for e in bad:
        print(e, file=sys.stderr)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
