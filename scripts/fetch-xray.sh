#!/usr/bin/env bash
# Кладёт в bin/xray закреплённый Xray-core для приёмочных тестов (core/acceptance, monitor, ext/rawhttp). Архив сверяется по SHA-256
# (значения совпадают с файлами .dgst на странице релиза). Уже есть bin/xray — ничего не делает (у пользователя там может лежать своя сборка).
#   scripts/fetch-xray.sh
set -euo pipefail
cd "$(dirname "$0")/.."
VER=v26.10.31
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) ASSET=Xray-linux-64.zip; SHA=e4bd9119f50ae7c41658b1abd0679ae06f98bd864a2907ff33f2f2cf7081b214; BIN=xray ;;
  MINGW*|MSYS*|CYGWIN*) ASSET=Xray-windows-64.zip; SHA=2e04a9995a6f2ad1b28c0bb25c708ecb2695e43a1c9c7889c4b6ac2b48cf7a6a; BIN=xray.exe ;;
  *) echo "fetch-xray: платформа $(uname -sm) не поддержана, bin/xray положите вручную" >&2; exit 1 ;;
esac
if [ -x bin/xray ] || [ -f bin/xray.exe ]; then exit 0; fi
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp/x.zip" "https://github.com/XTLS/Xray-core/releases/download/$VER/$ASSET"
echo "$SHA  $tmp/x.zip" | sha256sum -c - >/dev/null || { echo "fetch-xray: SHA-256 архива $ASSET не совпал" >&2; exit 1; }
mkdir -p bin
unzip -qo "$tmp/x.zip" "$BIN" -d bin
chmod +x "bin/$BIN"
echo "fetch-xray: bin/$BIN ($VER)"
