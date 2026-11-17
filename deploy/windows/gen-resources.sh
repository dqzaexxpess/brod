#!/usr/bin/env bash
# Ресурсы Windows (значок, сведения о файле, манифест) для cmd/brodsetup, cmd/brodd и cmd/brodctl: файлы resource_windows_amd64.syso
# рядом с main.go подхватывает `go build` под Windows. В репозиторий .syso не попадают: их создаёт сборка (CI) с номером версии.
# Инструмент goversioninfo скачивается с закреплённой версией (go.sum проверяет сумму модуля) и в зависимости проекта не входит.
#   deploy/windows/gen-resources.sh 0.3.0
set -euo pipefail
cd "$(dirname "$0")/../.."
ver=${1:?версия вида 0.3.0}
core=${ver%%[-+]*}
IFS=. read -r major minor patch <<<"$core"
patch=${patch:-0}
fv="$major.$minor.$patch.0"
icon=app/windows/runner/resources/app_icon.ico
tool=github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1
empty=$(mktemp)
echo '{}' >"$empty"
gen() { # каталог описание исходное_имя [манифест]
  local dir=$1 desc=$2 orig=$3 manifest=${4:-}
  local args=(-64 -company Brod -product-name Brod -description "$desc" -copyright "Copyright (C) 2026 Brod. GPL-3.0" -internal-name "$dir"
    -original-name "$orig" -file-version "$fv" -product-version "$fv" -ver-major "$major" -ver-minor "$minor" -ver-patch "$patch" -ver-build 0
    -product-ver-major "$major" -product-ver-minor "$minor" -product-ver-patch "$patch" -product-ver-build 0 -icon "$icon" -o "cmd/$dir/resource_windows_amd64.syso")
  if [ -n "$manifest" ]; then args+=(-manifest "$manifest"); fi
  go run "$tool" "${args[@]}" "$empty"
  echo "ресурсы: cmd/$dir/resource_windows_amd64.syso ($desc, $fv)"
}
gen brodsetup "Brod installer" "brod-$ver-windows-amd64-setup.exe" deploy/windows/brodsetup.manifest
gen brodd "Brod daemon" brodd.exe
gen brodctl "Brod command-line client" brodctl.exe
rm -f "$empty"
