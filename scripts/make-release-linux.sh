#!/usr/bin/env bash
# Архив релиза для Linux (его ставит кнопка «Установить» и `brodctl update install`): то же содержимое, что deploy/install-linux.sh кладёт в
# ~/.local/opt/brod, плюс метка BROD-RELEASE («версия платформа»). Версия — buildinfo.Version.
#   scripts/build.sh brodd brodctl brod-helper && scripts/build-app.sh && scripts/make-release-linux.sh   → dist/brod-ВЕРСИЯ-linux-amd64.tar.gz
# Дальше: otasign release … -asset linux-amd64=ФАЙЛ@URL, подпись и публикация.
set -euo pipefail
cd "$(dirname "$0")/.."
VER=$(sed -n 's/^const Version = "\(.*\)"$/\1/p' buildinfo/buildinfo.go)
[ -n "$VER" ] || { echo "не нашёл версию в buildinfo/buildinfo.go" >&2; exit 1; }
# по архиву должно быть видно, из какого коммита он собран; из грязного дерева релиз не собирается (BROD_ALLOW_DIRTY=1 — осознанное
# исключение, тогда в метке «dirty»). BROD-RELEASE трогать нельзя (updater.CheckLayout сверяет его целиком), коммит лежит в отдельном BROD-COMMIT.
COMMIT=$(git rev-parse HEAD)
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  [ "${BROD_ALLOW_DIRTY:-}" = 1 ] || { echo "дерево git не чистое: закоммитьте правки (или BROD_ALLOW_DIRTY=1, тогда метка будет dirty)" >&2; exit 1; }
  COMMIT="$COMMIT-dirty"
fi
BUNDLE=app/build/linux/x64/release/bundle
for f in "$BUNDLE/brod" bin/brodd bin/brodctl bin/brod-helper; do
  [ -x "$f" ] || { echo "нет $f — scripts/build.sh brodd brodctl brod-helper && scripts/build-app.sh" >&2; exit 1; }
done
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
cp -a "$BUNDLE/." "$STAGE/"
install -m 0755 bin/brodd bin/brodctl "$STAGE/"
# установка из архива (install.sh) и всё, что ей нужно: помощник обновлений, правила polkit и NetworkManager, удаление
mkdir -p "$STAGE/deploy"
install -m 0755 deploy/archive-install.sh "$STAGE/install.sh"
install -m 0755 deploy/archive-system.sh deploy/tun-setup.sh deploy/uninstall-linux.sh deploy/uninstall-helper.sh "$STAGE/deploy/"
install -m 0644 deploy/dev.godbtw.brod.policy deploy/NetworkManager-brod.conf "$STAGE/deploy/"
install -m 0755 bin/brod-helper "$STAGE/deploy/"
printf '%s linux-amd64\n' "$VER" > "$STAGE/BROD-RELEASE"
printf '%s\n' "$COMMIT" > "$STAGE/BROD-COMMIT"
mkdir -p dist
OUT=dist/brod-$VER-linux-amd64.tar.gz
# порядок и время фиксированы: одинаковые входы дают одинаковый архив
tar --sort=name --mtime='2026-01-01 00:00:00' --owner=0 --group=0 --numeric-owner -C "$STAGE" -cf - . | gzip -n -9 > "$OUT"
echo "$OUT ($(stat -c %s "$OUT") байт, sha256 $(sha256sum "$OUT" | cut -d' ' -f1))"
