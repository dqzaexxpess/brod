#!/usr/bin/env bash
# Сборка приложения Brod (Linux, release). Полный шрифт значков (--no-tree-shake-icons): «урезанный» шрифт при инкрементальных сборках
# оставался от старой сборки, и часть значков (Монитор, «копировать» …) показывалась пустым местом.
# После сборки движок в бандле патчится на частоту кадров монитора (BROD_VSYNC_HZ, по умолчанию 144): встройщик Flutter на Linux
# держит запасной vsync на зашитых 60 Гц — подробности в scripts/patch-engine-vsync.py. BROD_VSYNC_HZ=0 — не патчить.
set -euo pipefail
cd "$(dirname "$0")/../app"
../scripts/flutterw.sh build linux --release --no-tree-shake-icons "$@"
hz="${BROD_VSYNC_HZ:-144}"
lib=build/linux/x64/release/bundle/lib/libflutter_linux_gtk.so
if [ "$hz" != "0" ]; then
  cp linux/flutter/ephemeral/libflutter_linux_gtk.so "$lib" # чистая копия: патч всегда накладывается на оригинал
  python3 ../scripts/patch-engine-vsync.py "$lib" "$hz"
fi
echo "app/build/linux/x64/release/bundle/brod"
