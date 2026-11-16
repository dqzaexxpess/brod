#!/usr/bin/env bash
# Поиск секретов (gitleaks) по всей истории git и по рабочему дереву. Значения в выводе скрыты (--redact).
#   scripts/secretscan.sh        код возврата 1 — найдены совпадения; 3 — инструмента нет и скачать не удалось
# Исключения (тестовые ключи) — в .gitleaks.toml.
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/fetch-gitleaks.sh || exit 3
bin/gitleaks git --no-banner --redact --config .gitleaks.toml .
bin/gitleaks dir --no-banner --redact --config .gitleaks.toml .
