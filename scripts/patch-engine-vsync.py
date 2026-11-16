#!/usr/bin/env python3
"""Патч частоты кадров Flutter-движка на Linux: libflutter_linux_gtk.so -> нужные Гц.

Зачем: Linux-встройщик Flutter не передаёт движку собственный vsync, и кадры тактирует запасной
`VsyncWaiterFallback`, у которого интервал зашит как 1/60 с (16666666 нс). Из-за этого приложение
рисует не больше 60 кадров/с на мониторе любой частоты. Исходников встройщика в SDK нет, поэтому
правится готовая библиотека в собранном бандле (кэш SDK не трогаем).

Что меняется в `VsyncWaiterFallback::AwaitVSync`: три непосредственных значения 16666666 (деление с
остатком по сетке тиков и цель кадра) и деление на константу, которое компилятор заменил умножением на
«магическое число» со сдвигом — для нового интервала число и сдвиг пересчитываются.

Безопасность: ищется ровно один фрагмент машинного кода целиком (включая все байты между
заменяемыми); не найден или найден не один раз — выход с ошибкой и файл не меняется. Повторный
запуск на уже пропатченном файле — «уже применён» (по тому же шаблону с нужным интервалом).

Использование: patch-engine-vsync.py <файл.so> [Гц, по умолчанию 144]
"""
import random
import struct
import sys

OLD_NS = 16666666
OLD_MAGIC = 0x80D959A39B615CCF
OLD_SHIFT = 23
W = 64


def magic_signed(d):
    """Магическое число и сдвиг для знакового деления int64 на d>=2 (Hacker's Delight, 10-1)."""
    two = 1 << (W - 1)
    ad = d
    t = two
    anc = t - 1 - t % ad
    p = W - 1
    q1, r1 = divmod(two, anc)
    q2, r2 = divmod(two, ad)
    while True:
        p += 1
        q1, r1 = 2 * q1, 2 * r1
        if r1 >= anc:
            q1 += 1
            r1 -= anc
        q2, r2 = 2 * q2, 2 * r2
        if r2 >= ad:
            q2 += 1
            r2 -= ad
        delta = ad - r2
        if not (q1 < delta or (q1 == delta and r1 == 0)):
            break
    return (q2 + 1) & ((1 << W) - 1), p - W


def sdiv_model(n, magic, shift, add):
    """Модель кода компилятора: rdx = hi(M*n); [rdx += n]; q = (rdx >> s) + (rdx >> 63 без знака)."""
    m = magic - (1 << W) if magic >= 1 << (W - 1) else magic
    hi = (m * n) >> W
    if add:
        hi += n
    return (hi >> shift) + ((hi & ((1 << W) - 1)) >> (W - 1))


def trunc_div(n, d):
    q = abs(n) // d
    return q if n >= 0 else -q


def check_model(ns, magic, shift, add):
    rnd = random.Random(1)
    for n in [0, 1, -1, ns - 1, ns, ns + 1, -ns, -ns - 1, 10**9, -(10**9), 2**62, -(2**62), 2**63 - 1, -(2**63) + 1] + [
        rnd.randint(-(2**63) + 1, 2**63 - 1) for _ in range(200000)
    ] + [rnd.randint(-(10**15), 10**15) for _ in range(200000)]:
        if sdiv_model(n, magic, shift, add) != trunc_div(n, ns):
            raise SystemExit(f"модель деления неверна для n={n}, d={ns}")


def block(ns, magic, shift, add):
    """Байты участка AwaitVSync от movabs rdx,M до add r15,imm (без вызовов между ними)."""
    imm = struct.pack("<I", ns)
    b = b"\x48\xba" + struct.pack("<Q", magic)  # movabs rdx, M
    b += b"\x48\x89\xf0"  # mov rax, rsi
    b += b"\x48\xf7\xea"  # imul rdx
    b += b"\x48\x01\xf2" if add else b"\x0f\x1f\x00"  # add rdx, rsi | nop3
    b += b"\x48\x89\xd0"  # mov rax, rdx
    b += b"\x48\xc1\xe8\x3f"  # shr rax, 63
    b += b"\x48\xc1\xfa" + bytes([shift])  # sar rdx, s
    b += b"\x48\x01\xc2"  # add rdx, rax
    b += b"\x48\x69\xc2" + imm  # imul rax, rdx, ns
    b += b"\x48\x89\xf2"  # mov rdx, rsi
    b += b"\x48\x29\xc2"  # sub rdx, rax
    b += b"\x48\xf7\xd8"  # neg rax
    b += b"\x48\x85\xd2"  # test rdx, rdx
    b += b"\x48\x8d\x84\x06" + imm  # lea rax, [rsi+rax+ns]
    b += b"\x48\x0f\x44\xc2"  # cmove rax, rdx
    b += b"\x4c\x8d\x34\x08"  # lea r14, [rax+rcx]
    b += b"\x4c\x8d\x3c\x08"  # lea r15, [rax+rcx]
    b += b"\x49\x81\xc7" + imm  # add r15, ns
    return b


def main():
    if len(sys.argv) < 2:
        raise SystemExit(__doc__)
    path = sys.argv[1]
    hz = float(sys.argv[2]) if len(sys.argv) > 2 else 144.0
    if not 30 <= hz <= 1000:
        raise SystemExit("частота вне 30..1000 Гц")
    new_ns = round(1e9 / hz)
    magic, shift = magic_signed(new_ns)
    add = magic >= 1 << (W - 1)  # знаковое М отрицательно -> компилятор добавляет n
    check_model(OLD_NS, OLD_MAGIC, OLD_SHIFT, True)  # модель воспроизводит исходный код
    check_model(new_ns, magic, shift, add)

    data = bytearray(open(path, "rb").read())
    old = block(OLD_NS, OLD_MAGIC, OLD_SHIFT, True)
    new = block(new_ns, magic, shift, add)
    assert len(old) == len(new)
    if data.count(new) == 1 and data.count(old) == 0:
        print(f"уже применён: интервал {new_ns} нс ({1e9 / new_ns:.2f} Гц)")
        return
    if data.count(old) != 1:
        raise SystemExit(f"участок AwaitVSync найден {data.count(old)} раз(а) (нужен ровно один): другая версия движка, патч не применён")
    i = data.find(old)
    data[i : i + len(old)] = new
    open(path, "wb").write(data)
    print(f"применён: интервал кадра {OLD_NS} -> {new_ns} нс ({1e9 / new_ns:.2f} Гц), смещение 0x{i:x}")


if __name__ == "__main__":
    main()
