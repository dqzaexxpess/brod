#!/usr/bin/env bash
# Устанавливает brodd и brodctl в ~/.local/bin и пользовательский юнит в ~/.config/systemd/user.
# Без sudo и без изменений системы. Запуск сервиса — только с --enable.
set -euo pipefail
cd "$(dirname "$0")/.."
BIN="${HOME}/.local/bin"
UNIT_DIR="${HOME}/.config/systemd/user"
for b in brodd brodctl; do
  [ -x "bin/$b" ] || { echo "нет bin/$b — соберите: source scripts/env.sh && CGO_ENABLED=0 go build -tags \"\$(cat scripts/tags.txt)\" -o bin/$b ./cmd/$b" >&2; exit 1; }
done
mkdir -p "$BIN" "$UNIT_DIR"
install -m 0755 bin/brodd bin/brodctl "$BIN/"
# юнит по умолчанию запускает демон, поставленный помощником (/usr/local/lib/brod/brodd); здесь ставится своя сборка в ~/.local/bin
sed 's#^ExecStart=/usr/local/lib/brod/brodd #ExecStart=%h/.local/bin/brodd #' deploy/brodd.user.service > "$UNIT_DIR/brodd.service.tmp"
grep -q '^ExecStart=%h/.local/bin/brodd ' "$UNIT_DIR/brodd.service.tmp" || { rm -f "$UNIT_DIR/brodd.service.tmp"; echo "не удалось подставить путь в юнит" >&2; exit 1; }
chmod 0644 "$UNIT_DIR/brodd.service.tmp"
mv -f "$UNIT_DIR/brodd.service.tmp" "$UNIT_DIR/brodd.service"
CONF="${HOME}/.config/brod"
mkdir -p "$CONF"
if [ ! -e "$CONF/sources.txt" ]; then
  if [ -f work/sources-good.txt ]; then
    install -m 0600 work/sources-good.txt "$CONF/sources.txt"
    echo "источники: скопирован work/sources-good.txt → $CONF/sources.txt (продуктивные источники из Ф2)"
  else
    printf '# имя|url|приоритет (меньше = раньше); перечитывается по SIGHUP (systemctl --user reload brodd)\n' > "$CONF/sources.txt"
    chmod 0600 "$CONF/sources.txt"
    echo "источники: создан пустой $CONF/sources.txt (используются только источники по умолчанию)"
  fi
fi
echo "установлено: $BIN/brodd, $BIN/brodctl, $UNIT_DIR/brodd.service"
if [ "${1:-}" = "--enable" ]; then
  systemctl --user daemon-reload
  systemctl --user enable --now brodd
  echo "сервис включён и запущен: brodctl status"
else
  echo "чтобы включить: systemctl --user daemon-reload && systemctl --user enable --now brodd   (или повторите с --enable)"
fi
