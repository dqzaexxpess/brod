#!/usr/bin/env bash
# Сборка демона и CLI в bin/ с теми же тегами, что и в тестах. Использование: scripts/build.sh [brodd brodctl ...]
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh >/dev/null 2>&1 || true
export CGO_ENABLED=0
TAGS="$(cat scripts/tags.txt)"
targets=("$@"); [ ${#targets[@]} -gt 0 ] || targets=(brodd brodctl checklink)
mkdir -p bin
for t in "${targets[@]}"; do
  go build -tags "$TAGS" -trimpath -o "bin/$t" "./cmd/$t"
  echo "bin/$t"
done
