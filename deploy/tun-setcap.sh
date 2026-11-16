#!/usr/bin/env bash
# Выдаёт бинарнику brodd право создавать TUN и менять маршруты БЕЗ запуска от root: cap_net_admin (интерфейсы, маршруты)
# и cap_net_bind_service. Нужен sudo ОДИН раз после каждой пересборки bin/brodd (новый файл теряет права).
# Снять: sudo setcap -r bin/brodd
set -euo pipefail
cd "$(dirname "$0")/.."
[ -x bin/brodd ] || { echo "нет bin/brodd — scripts/build.sh" >&2; exit 1; }
sudo setcap cap_net_admin,cap_net_bind_service,cap_net_raw+ep bin/brodd
getcap bin/brodd
