#!/usr/bin/env bash
# Проверка зависимостей на известные уязвимости (govulncheck): базу vuln.go.dev с части выходов не отдают (403); тогда запускать через прокси:
#   HTTPS_PROXY=http://... scripts/vulncheck.sh
# Устанавливает govulncheck в $GOBIN (по умолчанию ~/go/bin), если его нет. Код возврата 3 — найдены уязвимости, которые код вызывает.
set -euo pipefail
# версия закреплена (не @latest); обновлять осознанно
GOVULNCHECK_VERSION=${GOVULNCHECK_VERSION:-v1.9.0}
cd "$(dirname "$0")/.."
bin=$(go env GOBIN); bin=${bin:-$(go env GOPATH)/bin}
[ -x "$bin/govulncheck" ] || go install golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION
"$bin/govulncheck" -tags "$(cat scripts/tags.txt)" ./cmd/... ./core/... ./api/... ./supervisor/... ./engine/... ./androidcore/... "$@"
