#!/usr/bin/env bash
# удаление помощника brod-helper и политики polkit (поставлены deploy/install-helper.sh). ЗАПУСКАТЬ ВАМ:
#
#   sudo deploy/uninstall-helper.sh           помощник и политика; демон в /usr/local/lib/brod остаётся (приложение продолжает работать,
#                                             следующее обновление пойдёт прежним способом — переходный режим)
#   sudo deploy/uninstall-helper.sh --purge   + каталог /usr/local/lib/brod с демоном; после этого: deploy/install-linux.sh (вернёт brodd
#                                             в ~/.local/opt/brod), иначе приложение не найдёт демон
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "запусти через sudo: sudo deploy/uninstall-helper.sh" >&2; exit 1; }
PURGE=0
for a in "$@"; do
  case "$a" in
    --purge) PURGE=1 ;;
    -h|--help) sed -n 2,7p "$0"; exit 0 ;;
    *) echo "неизвестный параметр: $a" >&2; exit 2 ;;
  esac
done
LIBEXEC=/usr/local/libexec/brod
LIB=/usr/local/lib/brod
rm -f "$LIBEXEC/brod-helper" /usr/share/polkit-1/actions/dev.godbtw.brod.policy
rm -f "$LIBEXEC"/.brod-helper.*
[ -d "$LIBEXEC" ] && rmdir "$LIBEXEC" 2>/dev/null || true
echo "удалены: помощник и политика polkit"
if [ "$PURGE" = 1 ]; then
  if pgrep -f "^$LIB/brodd" >/dev/null; then
    echo "демон из $LIB ещё работает: закройте Brod и повторите" >&2
    exit 1
  fi
  [ -d "$LIB" ] && rm -r "$LIB"
  echo "удалён $LIB; верните демон в каталог пользователя: deploy/install-linux.sh"
else
  [ -d "$LIB" ] && echo "оставлен $LIB (демон; убрать: sudo deploy/uninstall-helper.sh --purge)"
fi
