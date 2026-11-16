#!/usr/bin/env bash
# установка помощника от root brod-helper. ЗАПУСКАТЬ ВАМ, в обычном терминале:
#
#   scripts/build.sh brod-helper && sudo deploy/install-helper.sh [путь-к-brod-helper]
#
# Что делает (и больше ничего; повторный запуск безопасен):
#  1. /usr/local/libexec/brod/brod-helper (root:root 0755) — копия собранного помощника. Перед заменой проверяется, что в нём вшит боевой
#     ключ релизов (ota.ReleasesPublicKey из ota/releases.go): тестовую сборку с временным ключом поставить нельзя.
#  2. /usr/share/polkit-1/actions/dev.godbtw.brod.policy — действие dev.godbtw.brod.install: без пароля только в активной локальной сессии.
#  3. /usr/local/lib/brod (root:root 0755) — каталог, куда помощник ставит brodd (существующие файлы не трогаются).
# Удаление: sudo deploy/uninstall-helper.sh.
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "запусти через sudo: sudo deploy/install-helper.sh" >&2; exit 1; }
cd "$(dirname "$0")/.."
SRC=${1:-bin/brod-helper}
LIBEXEC=/usr/local/libexec/brod
HELPER=$LIBEXEC/brod-helper
LIB=/usr/local/lib/brod
POLICY=/usr/share/polkit-1/actions/dev.godbtw.brod.policy

[ -f "$SRC" ] && [ ! -L "$SRC" ] || { echo "нет $SRC (обычного файла) — соберите: scripts/build.sh brod-helper" >&2; exit 1; }
[ -x /usr/sbin/setcap ] || { echo "нет /usr/sbin/setcap (пакет libcap2-bin): помощнику нечем выдавать права" >&2; exit 1; }
command -v pkexec >/dev/null || echo "ВНИМАНИЕ: нет pkexec (пакет polkit / pkexec): приложение не сможет вызвать помощника" >&2
WANT=$(sed -n 's/^const ReleasesPublicKey = "\(.*\)"$/\1/p' ota/releases.go)
[ -n "$WANT" ] || { echo "не нашёл ReleasesPublicKey в ota/releases.go" >&2; exit 1; }

mkdir -p /usr/local/libexec
install -d -o root -g root -m 0755 "$LIBEXEC" "$LIB"
TMP=$(mktemp "$LIBEXEC/.brod-helper.XXXXXX")
trap 'rm -f "$TMP"' EXIT
install -o root -g root -m 0755 "$SRC" "$TMP"
GOT=$("$TMP" pubkey) || { echo "$SRC не запускается как помощник" >&2; exit 1; }
[ "$GOT" = "$WANT" ] || { echo "в $SRC вшит ключ $GOT, а не боевой ключ релизов (тестовая сборка?): не ставлю" >&2; exit 1; }
mv -f "$TMP" "$HELPER"
trap - EXIT
install -o root -g root -m 0644 deploy/dev.godbtw.brod.policy "$POLICY"

echo "готово:"
ls -l "$HELPER" "$POLICY"
ls -ld "$LIB"
echo "Дальше: deploy/install-linux.sh (без sudo; спросит сам) переносит демон в $LIB/brodd; обновления из приложения ставит помощник."
