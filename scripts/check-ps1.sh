#!/usr/bin/env bash
# Разбор deploy/windows/*.ps1 на ошибки синтаксиса. Есть pwsh/powershell — настоящий парсер PowerShell;
# нет (Linux) — статическая проверка scripts/check_ps1.py (баланс скобок, кавычек, here-строк).
#   scripts/check-ps1.sh [файл.ps1 ...]      без аргументов — все deploy/windows/*.ps1
set -euo pipefail
cd "$(dirname "$0")/.."
files=("$@")
[ ${#files[@]} -gt 0 ] || files=(deploy/windows/*.ps1)
ps=$(command -v pwsh || command -v powershell || true)
if [ -n "$ps" ]; then
  for f in "${files[@]}"; do
    "$ps" -NoProfile -Command '
      $e = $null; $t = $null
      [void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path $args[0]).Path, [ref]$t, [ref]$e)
      if ($e -and $e.Count) { $e | ForEach-Object { "$($args[0]):$($_.Extent.StartLineNumber): $($_.Message)" | Write-Error }; exit 1 }' "$f"
    echo "ps1 разобран: $f"
  done
else
  python3 -I scripts/check_ps1.py "${files[@]}"
  echo "ps1 (статическая проверка, без PowerShell): ${files[*]}"
fi
