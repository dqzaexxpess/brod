#!/usr/bin/env python3
"""Все значки Brod из одной геометрии (нужен Pillow): python3 scripts/gen-icons.py

Знак: щит, в который вырезаны две волны — «брод», переправа через воду. Плашка приложения: скруглённый квадрат с диагональным градиентом
от глубокого к светлому бирюзовому (#0E4A52 → #27B79A). Пишет:
  app/assets/app_icon.png                     окно Linux (256 px)
  app/windows/runner/resources/app_icon.ico   exe и ярлык Windows
  app/assets/tray_{off,connecting,on,problem}.png/.ico   трей и значок окна: плашка бренда со щитом и волнами, цвет по состоянию
  app/android/.../drawable/ic_launcher_foreground.xml, ic_launcher_bg.xml (градиент), ic_stat_brod.xml (уведомление)
  app/android/.../mipmap-*/ic_launcher.png    значки для Android до 8.0
"""
import math
import os
from PIL import Image, ImageDraw

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
RES = os.path.join(ROOT, "app/android/app/src/main/res")
SS = 4  # суперсэмплинг

C0 = (0x0E, 0x4A, 0x52)  # начало градиента плашки
C1 = (0x27, 0xB7, 0x9A)  # конец градиента плашки


def cubic(p0, p1, p2, p3, n=28):
    pts = []
    for i in range(1, n + 1):
        t = i / n
        u = 1 - t
        pts.append((u ** 3 * p0[0] + 3 * u * u * t * p1[0] + 3 * u * t * t * p2[0] + t ** 3 * p3[0],
                    u ** 3 * p0[1] + 3 * u * u * t * p1[1] + 3 * u * t * t * p2[1] + t ** 3 * p3[1]))
    return pts


# Щит в системе координат 256×256. Контур как в SVG-пути SHIELD_PATH ниже (кривые разобраны на точки для растра).
SHIELD = ([(128, 14), (226, 44), (226, 140)] + cubic((226, 140), (226, 190), (180, 222), (128, 244)) +
          cubic((128, 244), (76, 222), (30, 190), (30, 140)) + [(30, 44)])
SHIELD_PATH = "M128,14 L226,44 L226,140 C226,190 180,222 128,244 C76,222 30,190 30,140 L30,44 Z"


def ribbon(y0, phase, amp=10.0, wl=92.0, thick=19.0, x0=70.0, x1=186.0, step=4.0):
    """Волнистая лента с закруглёнными концами: многоугольник (внутри щита целиком)."""
    def top(x):
        return y0 + amp * math.sin(2 * math.pi * (x - x0) / wl + phase)
    xs = [x0 + i * step for i in range(int((x1 - x0) / step) + 1)]
    if xs[-1] < x1:
        xs.append(x1)
    up = [(x, top(x)) for x in xs]
    down = [(x, top(x) + thick) for x in reversed(xs)]
    r = thick / 2
    def cap(cx, cy, a0, a1, n=8):
        return [(cx + r * math.cos(a0 + (a1 - a0) * i / n), cy + r * math.sin(a0 + (a1 - a0) * i / n)) for i in range(1, n)]
    right = cap(x1, top(x1) + r, -math.pi / 2, math.pi / 2)
    left = cap(x0, top(x0) + r, math.pi / 2, 3 * math.pi / 2)
    return up + right + down + left


WAVES = [ribbon(100, 0.0), ribbon(146, math.pi)]


def poly_path(pts):
    return "M" + " L".join(f"{x:.1f},{y:.1f}" for x, y in pts) + " Z"


GLYPH_PATH = SHIELD_PATH + " " + " ".join(poly_path(w) for w in WAVES)  # fillType evenOdd: волны вырезаются


def gradient_tile(size):
    """Плашка: скруглённый квадрат с диагональным градиентом."""
    s = size * SS
    img = Image.new("RGBA", (s, s))
    px = img.load()
    for y in range(s):
        for x in range(s):
            t = (x + y) / (2.0 * (s - 1))
            px[x, y] = (round(C0[0] + (C1[0] - C0[0]) * t), round(C0[1] + (C1[1] - C0[1]) * t), round(C0[2] + (C1[2] - C0[2]) * t), 255)
    mask = Image.new("L", (s, s), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, s - 1, s - 1], radius=int(s * 0.225), fill=255)
    img.putalpha(mask)
    return img


def glyph_layer(size, scale, cx=128.0, cy=129.0):
    """Белый щит с вырезанными волнами на прозрачном слое size×size (в суперсэмплинге)."""
    s = size * SS
    k = s * scale / 230.0  # высота щита (14…244) занимает scale от стороны
    ox, oy = s / 2 - cx * k, s / 2 - cy * k
    tr = lambda pts: [(ox + x * k, oy + y * k) for x, y in pts]
    layer = Image.new("L", (s, s), 0)
    d = ImageDraw.Draw(layer)
    d.polygon(tr(SHIELD), fill=255)
    for w in WAVES:
        d.polygon(tr(w), fill=0)
    return layer


def app_icon(size, scale=0.62):
    tile = gradient_tile(size)
    white = Image.new("RGBA", tile.size, (255, 255, 255, 255))
    white.putalpha(glyph_layer(size, scale))
    tile.alpha_composite(white)
    return tile.resize((size, size), Image.LANCZOS)


# ---- трей и значок окна: та же плашка со щитом и волнами, цвет плашки по состоянию VPN ----
STATES = {
    "off": ((0x44, 0x4F, 0x5A), (0x8E, 0x99, 0xA3)),         # выключен: серая
    "connecting": ((0xB2, 0x6A, 0x0C), (0xF5, 0xB0, 0x2A)),  # подключаюсь: янтарная
    "on": (C0, C1),                                          # включён: бренд-градиент, как значок приложения
    "problem": ((0x86, 0x1C, 0x22), (0xEE, 0x4D, 0x52)),     # проблема: красная
}


def tile(size, c0, c1):
    """Плашка со скруглением и диагональным градиентом c0 → c1."""
    s_ = size * SS
    img = Image.new("RGBA", (s_, s_))
    px = img.load()
    for y in range(s_):
        for x in range(s_):
            t = (x + y) / (2.0 * (s_ - 1))
            px[x, y] = tuple(round(c0[i] + (c1[i] - c0[i]) * t) for i in range(3)) + (255,)
    mask = Image.new("L", (s_, s_), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, s_ - 1, s_ - 1], radius=int(s_ * 0.225), fill=255)
    img.putalpha(mask)
    return img


def tray_icon(c0, c1, size=64):
    # у трея щит крупнее, чем у значка приложения (0,62): на 16–24 px волны иначе сливаются
    img = tile(size, c0, c1)
    white = Image.new("RGBA", img.size, (255, 255, 255, 255))
    white.putalpha(glyph_layer(size, 0.74))
    img.alpha_composite(white)
    return img.resize((size, size), Image.LANCZOS)


# ---- Android ----
def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def android_vectors():
    # передний план адаптивного значка: 108dp, щит 62dp в безопасной зоне (66dp)
    kf = 0.27
    tx, ty = 54 - 128 * kf, 54 - 129 * kf
    write(os.path.join(RES, "drawable/ic_launcher_foreground.xml"), f'''<?xml version="1.0" encoding="utf-8"?>
<vector xmlns:android="http://schemas.android.com/apk/res/android"
    android:width="108dp" android:height="108dp" android:viewportWidth="108" android:viewportHeight="108">
    <group android:translateX="{tx:.2f}" android:translateY="{ty:.2f}" android:scaleX="{kf}" android:scaleY="{kf}">
        <path android:fillColor="#FFFFFF" android:fillType="evenOdd" android:pathData="{GLYPH_PATH}"/>
    </group>
</vector>
''')
    write(os.path.join(RES, "drawable/ic_launcher_bg.xml"), f'''<?xml version="1.0" encoding="utf-8"?>
<vector xmlns:android="http://schemas.android.com/apk/res/android" xmlns:aapt="http://schemas.android.com/aapt"
    android:width="108dp" android:height="108dp" android:viewportWidth="108" android:viewportHeight="108">
    <path android:pathData="M0,0h108v108h-108z">
        <aapt:attr name="android:fillColor">
            <gradient android:type="linear" android:startX="0" android:startY="0" android:endX="108" android:endY="108">
                <item android:offset="0" android:color="#{C0[0]:02X}{C0[1]:02X}{C0[2]:02X}"/>
                <item android:offset="1" android:color="#{C1[0]:02X}{C1[1]:02X}{C1[2]:02X}"/>
            </gradient>
        </aapt:attr>
    </path>
</vector>
''')
    ks = 0.088  # уведомление: белый силуэт 24dp
    write(os.path.join(RES, "drawable/ic_stat_brod.xml"), f'''<?xml version="1.0" encoding="utf-8"?>
<vector xmlns:android="http://schemas.android.com/apk/res/android"
    android:width="24dp" android:height="24dp" android:viewportWidth="24" android:viewportHeight="24">
    <group android:translateX="{12 - 128 * ks:.2f}" android:translateY="{12 - 129 * ks:.2f}" android:scaleX="{ks}" android:scaleY="{ks}">
        <path android:fillColor="#FFFFFF" android:fillType="evenOdd" android:pathData="{GLYPH_PATH}"/>
    </group>
</vector>
''')
    write(os.path.join(RES, "mipmap-anydpi-v26/ic_launcher.xml"), '''<?xml version="1.0" encoding="utf-8"?>
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@drawable/ic_launcher_bg"/>
    <foreground android:drawable="@drawable/ic_launcher_foreground"/>
    <!-- «Тематические значки» Android 13+: система красит силуэт щита в цвет темы -->
    <monochrome android:drawable="@drawable/ic_launcher_foreground"/>
</adaptive-icon>
''')
    for d, px in (("mdpi", 48), ("hdpi", 72), ("xhdpi", 96), ("xxhdpi", 144), ("xxxhdpi", 192)):
        app_icon(px).save(os.path.join(RES, f"mipmap-{d}/ic_launcher.png"))


if __name__ == "__main__":
    big = app_icon(256)
    big.save(os.path.join(ROOT, "app/assets/app_icon.png"))
    app_icon(1024).save(os.path.join(os.environ.get("ICON_PREVIEW_DIR", "/tmp"), "brod_icon_1024.png")) if os.environ.get("ICON_PREVIEW_DIR") else None
    ico = os.path.join(ROOT, "app/windows/runner/resources/app_icon.ico")
    app_icon(256).save(ico, format="ICO", sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])
    for name, (c0, c1) in STATES.items():
        im = tray_icon(c0, c1)
        im.save(os.path.join(ROOT, f"app/assets/tray_{name}.png"))
        im.save(os.path.join(ROOT, f"app/assets/tray_{name}.ico"), format="ICO", sizes=[(16, 16), (20, 20), (24, 24), (32, 32), (48, 48), (64, 64)])
    # цветовой ресурс плашки не нужен: фон — градиент (drawable/ic_launcher_bg.xml)
    old = os.path.join(RES, "values/ic_launcher_background.xml")
    if os.path.exists(old):
        os.remove(old)
    android_vectors()
    print("готово: app_icon.png/.ico, tray_*.png/.ico, Android: drawable/ic_launcher_{foreground,bg}.xml, ic_stat_brod.xml, mipmap-*")
