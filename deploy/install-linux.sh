#!/usr/bin/env bash
# Установка Brod для текущего пользователя: приложение, демон и CLI в ~/.local/opt/brod, команды в ~/.local/bin, ярлык и значок в меню.
# Системной службы и автозапуска нет: «запустил приложение — включил VPN» (приложение само запускает демон и гасит его при выходе).
#
#   scripts/build.sh && scripts/build-app.sh && deploy/install-linux.sh       # собрать и установить
#   deploy/install-linux.sh --build                                           # то же одной командой (обновление: повторить)
#   deploy/install-linux.sh --no-system                                       # без sudo: режим TUN работать не будет
#
# Шаги с sudo — права для режима TUN и DNS (deploy/tun-setup.sh): cap_net_admin на установленный brodd (после КАЖДОГО обновления файл новый,
# права нужно выдавать заново — скрипт делает это сам), правило NetworkManager и правило polkit для resolved. Он спросит пароль.
# если поставлен помощник (sudo deploy/install-helper.sh), демон кладётся в /usr/local/lib/brod/brodd (root:root, прежний остаётся
# brodd.prev), а ~/.local/opt/brod/brodd становится ссылкой на него: права получает файл, который пользователь не может переписать.
# Без помощника установка ставит его сама (sudo): обновления из приложения идут только через него.
#
# Переменные: BROD_PREFIX (по умолчанию ~/.local) — куда ставить; удобно для проверки установки без изменения домашнего каталога.
# Удаление: deploy/uninstall-linux.sh. Данные (узлы, выбор, WARP-аккаунт) в ~/.local/share/brod (прежний ~/.local/share/vpnclient переносится при первом запуске) не трогаются ни установкой, ни удалением
# (кроме uninstall --purge).
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
PREFIX=${BROD_PREFIX:-$HOME/.local}
BUILD=0; SYSTEM=1
for a in "$@"; do
  case "$a" in
    --build) BUILD=1 ;;
    --no-system) SYSTEM=0 ;;
    -h|--help) sed -n 2,17p "$0"; exit 0 ;;
    *) echo "неизвестный параметр: $a" >&2; exit 2 ;;
  esac
done
[ "$(id -u)" != 0 ] || { echo "запускай БЕЗ sudo (sudo спросит сам, когда понадобится)" >&2; exit 2; }

OPT=$PREFIX/opt/brod
BINDIR=$PREFIX/bin
APPS=$PREFIX/share/applications
ICONS=$PREFIX/share/icons/hicolor/256x256/apps
ID=dev.godbtw.brod
BUNDLE=app/build/linux/x64/release/bundle

if [ "$BUILD" = 1 ]; then scripts/build.sh brodd brodctl; scripts/build-app.sh; fi
for f in "$BUNDLE/brod" bin/brodd bin/brodctl; do
  [ -x "$f" ] || { echo "нет $f — соберите: scripts/build.sh && scripts/build-app.sh (или запустите с --build)" >&2; exit 1; }
done

# Подменять файлы под работающим приложением нельзя (библиотеки Flutter грузятся при старте, brodd занят).
if pgrep -x brod >/dev/null || pgrep -x brodd >/dev/null || pgrep -x vpnd >/dev/null; then
  echo "Brod или демон ещё работают. Закройте приложение (в трее: «Выход») и подождите несколько секунд, затем повторите." >&2
  exit 1
fi

mkdir -p "$OPT" "$BINDIR" "$APPS" "$ICONS"
rsync -a --delete "$BUNDLE/" "$OPT/"              # новые файлы встают атомарно по одному (rename), лишние старые удаляются
install -m 0755 bin/brodctl "$OPT/"
# помощник обновлений: файлы root:root (создать их может только root); нет — ставим (нужен sudo), прежнего способа без него больше нет.
# Демон тогда живёт в каталоге root, а здесь — ссылка на него.
LIB=/usr/local/lib/brod
HELPER=/usr/local/libexec/brod/brod-helper
helper_ok() { [ -d "$LIB" ] && [ -f "$HELPER" ] && [ "$(stat -c %u "$LIB")" = 0 ] && [ "$(stat -c %u "$HELPER")" = 0 ]; }
if [ "$SYSTEM" = 1 ] && ! helper_ok; then
  echo "Помощник обновлений не установлен: ставлю (нужен sudo; можно пропустить: --no-system)…"
  [ -x bin/brod-helper ] || scripts/build.sh brod-helper
  sudo deploy/install-helper.sh bin/brod-helper
fi
HELPER_MODE=0
if [ "$SYSTEM" = 1 ] && helper_ok; then
  HELPER_MODE=1
  ln -sfn "$LIB/brodd" "$OPT/brodd"
else
  install -m 0755 bin/brodd "$OPT/"                 # без системного шага (--no-system): обычный файл, режим TUN работать не будет
fi
for t in brod brodd brodctl; do ln -sfn "$OPT/$t" "$BINDIR/$t"; done
for t in vpnd vpnctl; do # прежние имена команд (до переименования в Brod): убираем только свои ссылки
  l=$BINDIR/$t
  [ -L "$l" ] && [ "$(readlink "$l")" = "$OPT/$t" ] && rm -f "$l"
done
install -m 0644 app/assets/app_icon.png "$ICONS/$ID.png"
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

if [ "$HELPER_MODE" = 1 ]; then
  echo
  echo "Демон в $LIB/brodd, права для режима TUN и DNS (нужен sudo; можно пропустить: --no-system)…"
  # локальная сборка не подписана, поэтому не через помощника, а явным sudo владельца (как deploy/tun-setup.sh для bin/brodd)
  sudo install -o root -g root -m 0755 "$ROOT/bin/brodd" "$LIB/.install-linux-brodd"
  if sudo test -e "$LIB/brodd"; then sudo ln -f "$LIB/brodd" "$LIB/brodd.prev"; fi
  sudo mv -f "$LIB/.install-linux-brodd" "$LIB/brodd"
  sudo "$ROOT/deploy/tun-setup.sh" "$LIB/brodd"
else
  echo
  echo "Режим TUN работать не будет, пока не выполнено:  sudo $ROOT/deploy/tun-setup.sh $OPT/brodd"
fi
echo
echo "Готово. Запуск: ярлык «Brod» в меню или команда brod."
