#!/usr/bin/env bash
# Проверка установки и удаления из архива выпуска на заглушках, без sudo и без настоящей сборки: в пробный префикс ставятся файлы, ярлык и
# ссылки, затем всё убирается. Системный шаг (sudo) здесь не выполняется: его проверяют руками на машине.
#   scripts/test-archive-install.sh
set -euo pipefail
cd "$(dirname "$0")/.."
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
SRC=$T/src
mkdir -p "$SRC/deploy" "$SRC/data/flutter_assets/assets" "$SRC/lib" "$T/shim"
for b in brod brodd brodctl; do printf '#!/bin/sh\nexit 0\n' > "$SRC/$b"; chmod +x "$SRC/$b"; done
printf 'x' > "$SRC/data/flutter_assets/assets/app_icon.png"
printf '0.0.0 linux-amd64\n' > "$SRC/BROD-RELEASE"
install -m 0755 deploy/archive-install.sh "$SRC/install.sh"
install -m 0755 deploy/archive-system.sh deploy/tun-setup.sh deploy/uninstall-linux.sh "$SRC/deploy/"
install -m 0644 deploy/dev.godbtw.brod.policy deploy/NetworkManager-brod.conf "$SRC/deploy/"
printf '#!/bin/sh\nexit 0\n' > "$SRC/deploy/brod-helper"; chmod +x "$SRC/deploy/brod-helper"
# pgrep-заглушка: на машине разработчика Brod может быть запущен, а установка отказывается ставить поверх работающего
printf '#!/bin/sh\nexit 1\n' > "$T/shim/pgrep"; chmod +x "$T/shim/pgrep"
export PATH=$T/shim:$PATH BROD_PREFIX=$T/prefix

fail() { echo "ОШИБКА: $*" >&2; exit 1; }
(cd "$SRC" && ./install.sh --no-system >"$T/out.txt" 2>&1) || { cat "$T/out.txt" >&2; fail "install.sh завершился с ошибкой"; }
for f in opt/brod/brod opt/brod/brodd opt/brod/BROD-RELEASE opt/brod/deploy/archive-system.sh share/applications/dev.godbtw.brod.desktop share/icons/hicolor/256x256/apps/dev.godbtw.brod.png; do
  [ -e "$T/prefix/$f" ] || fail "после установки нет $f"
done
for t in brod brodd brodctl; do
  [ "$(readlink "$T/prefix/bin/$t")" = "$T/prefix/opt/brod/$t" ] || fail "ссылка bin/$t неверна"
done
grep -q "^Exec=$T/prefix/opt/brod/brod\$" "$T/prefix/share/applications/dev.godbtw.brod.desktop" || fail "в ярлыке не тот Exec"
# повторная установка поверх (обновление вручную) не падает
(cd "$SRC" && ./install.sh --no-system >/dev/null 2>&1) || fail "повторная установка завершилась с ошибкой"
# BROD_PREFIX и системный шаг несовместимы: пробная установка не должна дойти до sudo
(cd "$SRC" && ./install.sh >/dev/null 2>&1) && fail "install.sh с BROD_PREFIX и системным шагом должен отказать"
# каталог без файлов выпуска отвергается
mkdir "$T/bad" && cp "$SRC/install.sh" "$T/bad/" && (cd "$T/bad" && ./install.sh --no-system >/dev/null 2>&1) && fail "неполный каталог принят"
"$T/prefix/opt/brod/deploy/uninstall-linux.sh" >/dev/null 2>&1 || fail "удаление завершилось с ошибкой"
[ ! -e "$T/prefix/opt/brod" ] && [ ! -e "$T/prefix/bin/brod" ] && [ ! -e "$T/prefix/share/applications/dev.godbtw.brod.desktop" ] || fail "после удаления остались файлы"
echo "установка из архива: ок"
