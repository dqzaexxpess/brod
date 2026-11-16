#!/usr/bin/env bash
# Удаление Brod, установленного deploy/install-linux.sh.
#
#   deploy/uninstall-linux.sh             # приложение, команды, ярлык (данные остаются)
#   deploy/uninstall-linux.sh --system    # + системные правила (NetworkManager, polkit; нужен sudo)
#   deploy/uninstall-linux.sh --purge     # + данные пользователя: узлы, выбор, WARP-аккаунт, настройки (спросит подтверждение)
#
# Перед удалением снимается киллсвитч, если правила остались.
set -euo pipefail
PREFIX=${BROD_PREFIX:-$HOME/.local}
OPT=$PREFIX/opt/brod
ID=dev.godbtw.brod
SYSTEM=0; PURGE=0
for a in "$@"; do
  case "$a" in
    --system) SYSTEM=1 ;;
    --purge) PURGE=1 ;;
    -h|--help) sed -n 2,9p "$0"; exit 0 ;;
    *) echo "неизвестный параметр: $a" >&2; exit 2 ;;
  esac
done
[ "$(id -u)" != 0 ] || { echo "запускай БЕЗ sudo" >&2; exit 2; }
# --purge берёт пути данных из окружения пользователя, а не из BROD_PREFIX: при пробной установке в чужой префикс он снёс бы настоящие данные
if [ "$PURGE" = 1 ] && [ -n "${BROD_PREFIX:-}" ]; then
  echo "--purge не совместим с BROD_PREFIX (удалил бы настоящие данные пользователя). Уберите BROD_PREFIX или удалите данные вручную." >&2
  exit 2
fi

if pgrep -x brod >/dev/null || pgrep -x brodd >/dev/null; then
  echo "Brod или демон ещё работают. Закройте приложение (в трее: «Выход») и повторите." >&2
  exit 1
fi
[ -x "$OPT/brodd" ] && "$OPT/brodd" --release-killswitch >/dev/null 2>&1 || true

for t in brod brodd brodctl vpnd vpnctl; do
  l=$PREFIX/bin/$t
  [ -L "$l" ] && [ "$(readlink "$l")" = "$OPT/$t" ] && rm -f "$l"   # только свои ссылки
done
rm -f "$PREFIX/share/applications/$ID.desktop" "$PREFIX/share/icons/hicolor/256x256/apps/$ID.png"
[ -d "$OPT" ] && rm -r "$OPT"
command -v update-desktop-database >/dev/null && update-desktop-database "$PREFIX/share/applications" 2>/dev/null || true
command -v kbuildsycoca6 >/dev/null && kbuildsycoca6 >/dev/null 2>&1 || true
echo "удалено: приложение, команды, ярлык"

if [ "$SYSTEM" = 1 ]; then
  sudo rm -f /etc/NetworkManager/conf.d/90-brod.conf /etc/NetworkManager/conf.d/90-vpnclient.conf /etc/polkit-1/rules.d/50-brod-dns.rules /etc/polkit-1/rules.d/50-vpnclient-dns.rules
  sudo systemctl reload NetworkManager 2>/dev/null || true
  echo "удалены системные правила (NetworkManager, polkit)"
fi

if [ "$PURGE" = 1 ]; then
  # новые каталоги и прежние (до переименования в Brod): если миграция ещё не прошла, данные лежат под старыми именами
  D=${XDG_DATA_HOME:-$HOME/.local/share}; K=${XDG_CONFIG_HOME:-$HOME/.config}
  DIRS=("$D/brod" "$D/vpnclient" "$K/brod" "$K/vpn-client")
  echo "Будут удалены данные (те из них, что существуют): ${DIRS[*]} (узлы, выбор, WARP-аккаунт, настройки приложения)."
  read -r -p "Введите yes для подтверждения: " ans
  if [ "$ans" = yes ]; then
    for d in "${DIRS[@]}"; do [ -d "$d" ] && rm -r "$d"; done
    echo "данные удалены"
  else
    echo "данные оставлены"
  fi
fi
