#!/usr/bin/env bash
# Воспроизводимо собирает исходники движка в work/src/lx: клон sing-box-lx по зафиксированному тегу/коммиту,
# нужные сабмодули (shallow) и наши патчи из patches/lx/*.patch. После этого go.mod (replace => ./work/src/lx) собирается обычным образом.
#
#   scripts/fetch-engine.sh            # если work/src/lx уже есть — только проверяет/досаживает патчи
set -euo pipefail
cd "$(dirname "$0")/.."
# Без перевода строк: на Windows с core.autocrlf=true исходники lx клонировались с CRLF, а копия sing из кэша Go оставалась с LF,
# и патчи (в репозитории — LF, см. .gitattributes) переставали накладываться. Для всех вызовов git в этом скрипте, включая сабмодули.
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.autocrlf GIT_CONFIG_VALUE_0=false
TAG=v1.14.4-lx.35
COMMIT=f1bac271853eaf13398ce1030c948aebc3aed2e1
DIR=work/src/lx
if [ ! -d "$DIR/.git" ]; then
  mkdir -p work/src
  git clone --depth 1 --branch "$TAG" https://github.com/Leadaxe/sing-box-lx.git "$DIR"
fi
[ "$(git -C "$DIR" rev-parse HEAD)" = "$COMMIT" ] || { echo "ожидался коммит $COMMIT, а в $DIR $(git -C "$DIR" rev-parse HEAD)"; exit 1; }
git -C "$DIR" submodule update --init --depth 1 submodules/wireguard-go submodules/sing-tun submodules/gvisor submodules/utls
for p in patches/lx/*.patch; do
  if git -C "$DIR" apply --check -R "../../../$p" 2>/dev/null; then
    echo "уже применён: $p"
  else
    # не «&& echo»: под set -e левая часть списка отказ не прерывает, и патч, переставший накладываться (смена тега), пропускался молча
    git -C "$DIR" apply "../../../$p" || { echo "патч не накладывается: $p" >&2; exit 1; }
    echo "применён: $p"
  fi
done
# коммит движка сверен выше, но лишние локальные правки в дереве сверкой коммита не видны: изменённые отслеживаемые файлы (в самом дереве
# и в сабмодулях) должны входить в перечень файлов наших патчей. Осознанное исключение: BROD_ENGINE_DIRTY_OK=1.
allowed=$(grep -h '^+++ b/' patches/lx/*.patch | sed 's|^+++ b/||' | sort -u)
dirty=$({ git -C "$DIR" status --porcelain --untracked-files=no --ignore-submodules=all | sed 's/^...//'
          git -C "$DIR" submodule foreach --quiet 'git status --porcelain --untracked-files=no | sed "s|^...|$sm_path/|"'; } | sort -u)
extra=$(comm -13 <(printf '%s\n' "$allowed") <(printf '%s\n' "$dirty") | sed '/^$/d')
if [ -n "$extra" ] && [ "${BROD_ENGINE_DIRTY_OK:-}" != 1 ]; then
  echo "в $DIR есть правки вне patches/lx (они попадут в сборку молча):" >&2
  printf '  %s\n' $extra >&2
  echo "верните файлы (git checkout) или оформите патчем; BROD_ENGINE_DIRTY_OK=1 — пропустить проверку" >&2
  exit 1
fi

# sing: копия модуля из кэша Go (версия из go.mod) в work/src/sing и наши патчи patches/sing/*.patch (go.mod: replace => ./work/src/sing)
SING_VER=$(sed -n 's/^\tgithub.com\/sagernet\/sing \(v[^ ]*\).*/\1/p' go.mod | head -1)
if [ ! -d work/src/sing/.git ]; then
  SRC=$(GOFLAGS=-mod=mod go mod download -json "github.com/sagernet/sing@$SING_VER" | sed -n 's/.*"Dir": "\(.*\)",/\1/p')
  [ -d "$SRC" ] || { echo "не удалось получить github.com/sagernet/sing@$SING_VER"; exit 1; }
  rm -rf work/src/sing
  cp -r "$SRC" work/src/sing
  chmod -R u+w work/src/sing
  git -C work/src/sing init -q
  git -C work/src/sing add -A >/dev/null
  git -C work/src/sing -c user.name=brod -c user.email=brod@localhost commit -qm base
fi
for p in patches/sing/*.patch; do
  if git -C work/src/sing apply --check -R "../../../$p" 2>/dev/null; then
    echo "уже применён: $p"
  else
    git -C work/src/sing apply "../../../$p" || { echo "патч не накладывается: $p" >&2; exit 1; }
    echo "применён: $p"
  fi
done
