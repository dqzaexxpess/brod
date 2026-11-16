#!/usr/bin/env bash
# Локальный прогон проверок: Go-тесты с -race, кросс-сборка Windows и Android (vet), Flutter analyze и test.
#   scripts/ci-local.sh          всё;  go — только Go;  flutter — только Flutter;  security — gitleaks и govulncheck
# Известно: acceptance-тесты (xray) нестабильны при параллельном запуске многих пакетов, поэтому -p 2; TestWireGuardEndpointProbe ходит
# во внешнюю сеть и может падать без неё.
set -euo pipefail
cd "$(dirname "$0")/.."
TAGS="$(cat scripts/tags.txt)"
# CI=1: тесты, которым нужен bin/xray, при его отсутствии падают, а не пропускаются (internal/xraybin)
export CI=1
what=${1:-all}

# Место: сборка -race с кэшем занимает несколько ГБ, а /tmp (tmpfs) забивается параллельными сборками; при нехватке go падает невнятно
# (SIGBUS, «no space left on device»). Временные файлы go держим на диске, до начала проверяем запас.
export GOTMPDIR=${GOTMPDIR:-$HOME/.cache/brod-gotmp}
mkdir -p "$GOTMPDIR"
need_gb=${BROD_MIN_FREE_GB:-8}
free_gb=$(( $(df -Pk "$GOTMPDIR" | awk 'NR==2 {print $4}') / 1048576 ))
if [ "$free_gb" -lt "$need_gb" ]; then
  echo "ci-local: свободно ${free_gb} ГБ, нужно не меньше ${need_gb} (BROD_MIN_FREE_GB). Освободи место: go clean -cache; rm -rf ~/.cache/brod-gotmp ~/.cache/gotmp-*" >&2
  exit 3
fi

go_checks() {
  scripts/fetch-engine.sh >/dev/null
  scripts/fetch-xray.sh
  # work/ не обходим (в нём бывают каталоги root с запретом чтения: find иначе завершается ошибкой)
  pkgs=$(find . \( -path ./work -o -path ./app -o -path ./.git \) -prune -o -name '*_test.go' -printf '%h\n' | sort -u)
  echo "== go test -race"
  CGO_ENABLED=1 go test -race -count=1 -p 2 -tags "$TAGS" $pkgs
  echo "== сборка под Windows"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags "$TAGS" -trimpath -ldflags=-checklinkname=0 -o /dev/null ./cmd/brodd
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags "$TAGS" -trimpath -o /dev/null ./cmd/brodctl
  echo "== тесты Windows компилируются (vet) и разбор .ps1"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet -tags "$TAGS" ./killswitch ./store ./memwatch ./paths ./cmd/brodd ./cmd/brod-helper ./updater
  scripts/check-ps1.sh
  echo "== vet Android"
  GOOS=android GOARCH=arm64 go vet -tags "$TAGS" ./androidcore
}

flutter_checks() {
  echo "== flutter analyze и test"
  (cd app && ../scripts/flutterw.sh pub get >/dev/null && ../scripts/flutterw.sh analyze && ../scripts/flutterw.sh test)
}

# gitleaks (история и дерево) и govulncheck. Нет инструмента или сети (vuln.go.dev с части выходов отдаёт 403) — шаг пропускается
# с пояснением; BROD_REQUIRE_SEC=1 превращает пропуск в ошибку (как в CI). Найденные секреты и уязвимости роняют проверку всегда.
security_checks() {
  local rc
  echo "== gitleaks"
  rc=0; scripts/secretscan.sh || rc=$?
  case $rc in
    0) ;;
    3) skip_sec "gitleaks не скачан (нет сети?)" ;;
    *) echo "ci-local: gitleaks нашёл совпадения (код $rc)" >&2; return 1 ;;
  esac
  echo "== govulncheck"
  scripts/fetch-engine.sh >/dev/null
  rc=0; scripts/vulncheck.sh || rc=$?
  case $rc in
    0) ;;
    3) echo "ci-local: govulncheck нашёл вызываемые уязвимости" >&2; return 1 ;;
    *) skip_sec "govulncheck не отработал (код $rc: нет сети, база vuln.go.dev недоступна, HTTPS_PROXY?)" ;;
  esac
}
skip_sec() {
  echo "ci-local: ПРОПУЩЕНО: $1" >&2
  [ "${BROD_REQUIRE_SEC:-0}" = 1 ] && { echo "ci-local: BROD_REQUIRE_SEC=1, пропуск считается ошибкой" >&2; exit 4; }
  return 0
}

case "$what" in
  go) go_checks ;;
  flutter) flutter_checks ;;
  security) security_checks ;;
  all) go_checks; flutter_checks; security_checks ;;
  *) echo "использование: $0 [all|go|flutter|security]"; exit 2 ;;
esac
echo "ci-local: всё зелёное"
