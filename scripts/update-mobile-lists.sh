#!/usr/bin/env bash
# Обновляет встроенный снимок белых списков мобильного интернета (mobile/data/*.txt.gz) из hxehex/russia-mobile-internet-whitelist.
# После обновления: go test ./mobile, коммит. Списки меняются часто, снимок устаревает: раз в несколько недель.
set -euo pipefail
cd "$(dirname "$0")/.."
base=https://raw.githubusercontent.com/hxehex/russia-mobile-internet-whitelist/main
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for f in whitelist ipwhitelist cidrwhitelist; do
  curl -fsS --max-time 60 -o "$tmp/$f.txt" "$base/$f.txt"
  [ "$(wc -l < "$tmp/$f.txt")" -gt 100 ] || { echo "$f.txt: слишком короткий, не заменяю" >&2; exit 1; }
done
for f in whitelist ipwhitelist cidrwhitelist; do gzip -9 -c "$tmp/$f.txt" > "mobile/data/$f.txt.gz"; done
echo "обновлено: $(date +%F)"; go test -count=1 ./mobile
