#!/usr/bin/env bash
# Архив релиза для Windows (его ставит кнопка «Установить» и `brodctl update install`): тот же состав, что и у сборки в один каталог, плюс метка BROD-RELEASE («версия платформа»). Демон и CLI собираются здесь (кросс-сборка, как в CI), приложение
# (Brod.exe, Flutter) на Linux не собрать: его каталог Release берётся с Windows, где его собрали из того же коммита:
#   (на Windows)  cd app && flutter build windows --release        → app/build/windows/x64/runner/Release
#   (на Linux)    scripts/make-release-windows.sh --app КАТАЛОГ_RELEASE --wintun wintun.dll [--version X.Y.Z] [--allow-stale-app]
# → dist/brod-ВЕРСИЯ-windows-amd64.zip. wintun.dll — из wintun-0.14.1.zip (SHA-256 в build.yml), в репозитории его нет.
# Версия по умолчанию — buildinfo.Version; --version нужен только для проверочного релиза (тест OTA поверх установленной версии), демон
# при этом остаётся со своей версией. Дальше: otasign release … -asset windows-amd64=ФАЙЛ@URL, подпись и публикация.
set -euo pipefail
cd "$(dirname "$0")/.."
APP=""; WINTUN=""; VER=""; STALE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --app) APP=${2:?}; shift 2 ;;
    --wintun) WINTUN=${2:?}; shift 2 ;;
    --version) VER=${2:?}; shift 2 ;;
    --allow-stale-app) STALE=1; shift ;;
    -h|--help) sed -n 2,11p "$0"; exit 0 ;;
    *) echo "неизвестный параметр: $1" >&2; exit 2 ;;
  esac
done
[ -n "$APP" ] && [ -n "$WINTUN" ] || { sed -n 2,11p "$0" >&2; exit 2; }
[ -n "$VER" ] || VER=$(sed -n 's/^const Version = "\(.*\)"$/\1/p' buildinfo/buildinfo.go)
[[ "$VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "версия должна быть вида X.Y.Z: $VER" >&2; exit 1; }
for f in "$APP/Brod.exe" "$APP/data/app.so" "$WINTUN" deploy/windows/install.ps1 deploy/windows/uninstall.ps1; do
  [ -f "$f" ] || { echo "нет $f" >&2; exit 1; }
done
# приложение должно быть собрано после последнего изменения app/ (иначе в релизе окажется старый интерфейс)
if [ "$STALE" != 1 ]; then
  last=$(git log -1 --format=%ct -- app)
  built=$(stat -c %Y "$APP/data/app.so")
  [ "$built" -ge "$last" ] || { echo "приложение в $APP собрано раньше последнего изменения app/ в git: пересоберите на Windows (или --allow-stale-app для проверочного архива)" >&2; exit 1; }
fi
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
cp -a "$APP/." "$STAGE/"
TAGS=$(cat scripts/tags.txt)
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
# демон как GUI-приложение (без окна консоли), как в CI
go build -tags "$TAGS" -trimpath -ldflags="-checklinkname=0 -H windowsgui" -o "$STAGE/brodd.exe" ./cmd/brodd
go build -tags "$TAGS" -trimpath -o "$STAGE/brodctl.exe" ./cmd/brodctl
cp "$WINTUN" "$STAGE/wintun.dll"
cp deploy/windows/install.ps1 deploy/windows/uninstall.ps1 "$STAGE/"
printf '%s windows-amd64\n' "$VER" > "$STAGE/BROD-RELEASE"
mkdir -p dist
OUT=$PWD/dist/brod-$VER-windows-amd64.zip
rm -f "$OUT"
(cd "$STAGE" && find . -type f | LC_ALL=C sort | sed 's|^\./||' | TZ=UTC zip -X -q -9 "$OUT" -@)
echo "$OUT ($(stat -c %s "$OUT") байт, sha256 $(sha256sum "$OUT" | cut -d' ' -f1))"
