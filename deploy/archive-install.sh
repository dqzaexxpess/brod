#!/usr/bin/env bash
# Установка Brod из готового архива выпуска (brod-<версия>-linux-amd64.tar.gz). Запускать из распакованного каталога, БЕЗ sudo
# (sudo спросит сам, когда понадобится):
#
#   tar xzf brod-<версия>-linux-amd64.tar.gz -C brod && cd brod && ./install.sh
#   ./install.sh --no-system      # без sudo: приложение, команды и ярлык; режим TUN работать не будет
#
# Что делает:
#  1. приложение, демон и CLI копируются в ~/.local/opt/brod, команды — в ~/.local/bin, ярлык и значок — в меню;
#  2. шаг с sudo (deploy/archive-system.sh): помощник обновлений от root (/usr/local/libexec/brod, правило polkit), демон в
#     /usr/local/lib/brod/brodd (root:root, права cap_net_admin для TUN), правило NetworkManager и polkit для DNS (deploy/tun-setup.sh).
#     Демон в каталоге пользователя становится ссылкой на этот файл: права получает файл, который пользователь не может переписать.
# Обновления дальше ставятся из приложения кнопкой «Установить». Удаление: deploy/uninstall-linux.sh [--system] [--purge].
set -euo pipefail
SRC=$(cd "$(dirname "$0")" && pwd)
PREFIX=${BROD_PREFIX:-$HOME/.local}
SYSTEM=1
for a in "$@"; do
  case "$a" in
    --no-system) SYSTEM=0 ;;
    -h|--help) sed -n 2,14p "$0"; exit 0 ;;
    *) echo "неизвестный параметр: $a" >&2; exit 2 ;;
  esac
done
[ "$(id -u)" != 0 ] || { echo "запускай БЕЗ sudo (sudo спросит сам, когда понадобится)" >&2; exit 2; }
for f in brod brodd brodctl BROD-RELEASE deploy/archive-system.sh deploy/tun-setup.sh deploy/brod-helper deploy/dev.godbtw.brod.policy deploy/NetworkManager-brod.conf; do
  [ -e "$SRC/$f" ] || { echo "в $SRC нет $f: это не каталог выпуска Brod" >&2; exit 1; }
done
[ -n "${BROD_PREFIX:-}" ] && [ "$SYSTEM" = 1 ] && { echo "BROD_PREFIX (пробная установка) несовместим с системным шагом: добавьте --no-system" >&2; exit 2; }

OPT=$PREFIX/opt/brod
BINDIR=$PREFIX/bin
APPS=$PREFIX/share/applications
ICONS=$PREFIX/share/icons/hicolor/256x256/apps
ID=dev.godbtw.brod
LIB=/usr/local/lib/brod

if [ "$SRC" = "$OPT" ]; then
  echo "install.sh запущен из каталога установки: распакуйте архив в другое место" >&2
  exit 1
fi
# Подменять файлы под работающим приложением нельзя (библиотеки Flutter грузятся при старте, brodd занят).
if pgrep -x brod >/dev/null || pgrep -x brodd >/dev/null; then
  echo "Brod или демон ещё работают. Закройте приложение (в трее: «Выход») и подождите несколько секунд, затем повторите."
  exit 1
fi

mkdir -p "$OPT" "$BINDIR" "$APPS" "$ICONS"
if command -v rsync >/dev/null; then
  rsync -a --delete "$SRC/" "$OPT/"   # новые файлы встают по одному (rename), лишние старые удаляются
else
  find "$OPT" -mindepth 1 -delete
  cp -a "$SRC/." "$OPT/"
fi
for t in brod brodd brodctl; do ln -sfn "$OPT/$t" "$BINDIR/$t"; done
install -m 0644 "$SRC/data/flutter_assets/assets/app_icon.png" "$ICONS/$ID.png"
cat > "$APPS/$ID.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Brod
GenericName=VPN
Comment=Личный VPN-клиент
Exec=$OPT/brod
Icon=$ID
Terminal=false
Categories=Network;
StartupWMClass=$ID
StartupNotify=false
EOF
command -v update-desktop-database >/dev/null && update-desktop-database "$APPS" 2>/dev/null || true
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t "$PREFIX/share/icons/hicolor" 2>/dev/null || true
command -v kbuildsycoca6 >/dev/null && kbuildsycoca6 >/dev/null 2>&1 || true

echo "установлено: $OPT"
echo "  команды:  $BINDIR/brod  $BINDIR/brodctl  $BINDIR/brodd   (убедитесь, что $BINDIR в PATH)"
echo "  ярлык:    $APPS/$ID.desktop"

if [ "$SYSTEM" = 1 ]; then
  echo
  echo "Системный шаг (нужен sudo): помощник обновлений, демон в $LIB, права для режима TUN и DNS…"
  sudo "$OPT/deploy/archive-system.sh"
  ln -sfn "$LIB/brodd" "$OPT/brodd"   # права получает файл root; здесь ссылка на него
else
  echo
  echo "Режим TUN работать не будет, пока не выполнено:  sudo $OPT/deploy/archive-system.sh && ln -sfn $LIB/brodd $OPT/brodd"
fi
echo
echo "Готово. Запуск: ярлык «Brod» в меню или команда brod."
