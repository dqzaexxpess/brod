#!/usr/bin/env bash
# Системный шаг установки из архива (его вызывает install.sh через sudo; повторный запуск безопасен):
#  1. /usr/local/libexec/brod/brod-helper (root:root 0755): помощник обновлений; перед заменой проверяется, что он запускается;
#  2. /usr/share/polkit-1/actions/dev.godbtw.brod.policy: действие помощника, без пароля только в активной локальной сессии;
#  3. /usr/local/lib/brod/brodd (root:root 0755): демон; прежний остаётся как brodd.prev;
#  4. deploy/tun-setup.sh: cap_net_admin для brodd, правило NetworkManager и polkit для DNS (подробности в самом скрипте).
# Файлы берутся из каталога выпуска, в котором лежит этот скрипт (deploy/ рядом с brodd).
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "запускать через sudo: install.sh сам вызывает этот скрипт" >&2; exit 1; }
[ -n "${SUDO_USER:-}" ] || { echo "нужен запуск через sudo из-под обычного пользователя (права DNS выдаются ему)" >&2; exit 1; }
ROOT=$(cd "$(dirname "$0")/.." && pwd)
LIBEXEC=/usr/local/libexec/brod
HELPER=$LIBEXEC/brod-helper
LIB=/usr/local/lib/brod
POLICY=/usr/share/polkit-1/actions/dev.godbtw.brod.policy

for f in brodd deploy/brod-helper deploy/dev.godbtw.brod.policy deploy/tun-setup.sh deploy/NetworkManager-brod.conf; do
  [ -f "$ROOT/$f" ] && [ ! -L "$ROOT/$f" ] || { echo "нет обычного файла $ROOT/$f" >&2; exit 1; }
done
[ -x /usr/sbin/setcap ] || { echo "нет /usr/sbin/setcap (пакет libcap2-bin): демону нечем выдавать права" >&2; exit 1; }
command -v pkexec >/dev/null || echo "ВНИМАНИЕ: нет pkexec (пакет polkit): приложение не сможет вызвать помощника обновлений" >&2

mkdir -p /usr/local/libexec
install -d -o root -g root -m 0755 "$LIBEXEC" "$LIB"

TMP=$(mktemp "$LIBEXEC/.brod-helper.XXXXXX")
trap 'rm -f "$TMP" "$LIB/.brodd.new"' EXIT
install -o root -g root -m 0755 "$ROOT/deploy/brod-helper" "$TMP"
"$TMP" pubkey >/dev/null || { echo "deploy/brod-helper не запускается как помощник" >&2; exit 1; }
mv -f "$TMP" "$HELPER"
install -o root -g root -m 0644 "$ROOT/deploy/dev.godbtw.brod.policy" "$POLICY"

install -o root -g root -m 0755 "$ROOT/brodd" "$LIB/.brodd.new"
if [ -e "$LIB/brodd" ]; then ln -f "$LIB/brodd" "$LIB/brodd.prev"; fi
mv -f "$LIB/.brodd.new" "$LIB/brodd"
trap - EXIT

"$ROOT/deploy/tun-setup.sh" "$LIB/brodd"
echo "готово: помощник $HELPER, демон $LIB/brodd"
