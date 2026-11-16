#!/usr/bin/env bash
# Кладёт в bin/gitleaks закреплённый gitleaks для поиска секретов. Архив сверяется по SHA-256 из gitleaks_<версия>_checksums.txt
# релиза (значение вписано сюда; при смене версии сверить заново). Уже есть bin/gitleaks — ничего не делает.
#   scripts/fetch-gitleaks.sh
set -euo pipefail
cd "$(dirname "$0")/.."
VER=8.28.0
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) ASSET=gitleaks_${VER}_linux_x64.tar.gz; SHA=a65b5253807a68ac0cafa4414031fd740aeb55f54fb7e55f386acb52e6a840eb ;;
  *) echo "fetch-gitleaks: платформа $(uname -sm) не поддержана, bin/gitleaks положите вручную" >&2; exit 1 ;;
esac
if [ -x bin/gitleaks ]; then exit 0; fi
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp/g.tgz" "https://github.com/gitleaks/gitleaks/releases/download/v$VER/$ASSET"
echo "$SHA  $tmp/g.tgz" | sha256sum -c - >/dev/null || { echo "fetch-gitleaks: SHA-256 архива $ASSET не совпал" >&2; exit 1; }
mkdir -p bin
tar xzf "$tmp/g.tgz" -C bin gitleaks
chmod +x bin/gitleaks
echo "fetch-gitleaks: bin/gitleaks (v$VER)"
