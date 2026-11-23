#!/usr/bin/env bash
# Настоящая установка и удаление из архива выпуска в чистом контейнере Ubuntu (нужен docker): системный шаг с sudo выполняется по-настоящему,
# в отличие от scripts/test-archive-install.sh (заглушки). Два сценария: минимальная система без NetworkManager и polkit и система, где их каталоги есть.
# Скрипты из deploy/ берутся из рабочего дерева поверх архива: так проверяются правки до выпуска.
#
#   scripts/test-archive-container.sh [архив.tar.gz]      (по умолчанию dist/brod-<версия>-linux-amd64.tar.gz)
set -euo pipefail
cd "$(dirname "$0")/.."
VER=$(sed -n 's/^const Version = "\(.*\)"$/\1/p' buildinfo/buildinfo.go)
ARC=${1:-dist/brod-$VER-linux-amd64.tar.gz}
[ -f "$ARC" ] || { echo "нет архива $ARC (scripts/make-release-linux.sh)" >&2; exit 1; }
command -v docker >/dev/null || { echo "нужен docker" >&2; exit 1; }
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
cp "$ARC" "$T/brod.tar.gz"
cp -r deploy "$T/deploy-src"
chmod -R a+rX "$T" # внутри контейнера читает обычный пользователь
cat > "$T/run.sh" <<'IN'
set -u
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq sudo libcap2-bin procps >/dev/null 2>&1
useradd -m u && echo 'u ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/u
fail() { echo "ОШИБКА: $*" >&2; exit 1; }
for scenario in minimal with-nm; do
  echo "=== сценарий: $scenario"
  if [ "$scenario" = with-nm ]; then mkdir -p /etc/NetworkManager/conf.d /etc/polkit-1/rules.d; fi
  su u -c '
    set -eu
    rm -rf ~/brod ~/.local/opt/brod && mkdir -p ~/brod && tar xzf /a/brod.tar.gz -C ~/brod
    cp /a/deploy-src/*.sh ~/brod/deploy/ && cp /a/deploy-src/archive-install.sh ~/brod/install.sh
    cd ~/brod && ./install.sh >/tmp/install.log 2>&1 || { cat /tmp/install.log; exit 1; }
  ' || fail "$scenario: install.sh завершился с ошибкой"
  [ -L /home/u/.local/opt/brod/brodd ] && [ "$(readlink /home/u/.local/opt/brod/brodd)" = /usr/local/lib/brod/brodd ] || fail "$scenario: brodd пользователя не ссылка на /usr/local/lib/brod/brodd"
  getcap /usr/local/lib/brod/brodd | grep -q cap_net_admin || fail "$scenario: у демона нет cap_net_admin"
  [ -x /usr/local/libexec/brod/brod-helper ] || fail "$scenario: нет помощника"
  [ -f /usr/share/polkit-1/actions/dev.godbtw.brod.policy ] || fail "$scenario: нет политики polkit"
  if [ "$scenario" = with-nm ]; then
    [ -f /etc/NetworkManager/conf.d/90-brod.conf ] && [ -f /etc/polkit-1/rules.d/50-brod-dns.rules ] || fail "$scenario: нет правил NetworkManager или polkit"
  fi
  # повторная установка поверх безопасна
  su u -c 'cd ~/brod && ./install.sh >/tmp/install2.log 2>&1' || fail "$scenario: повторная установка упала"
  su u -c '~/.local/opt/brod/deploy/uninstall-linux.sh --system >/tmp/uninstall.log 2>&1' || { cat /tmp/uninstall.log; fail "$scenario: удаление упало"; }
  [ ! -e /usr/local/lib/brod ] && [ ! -e /usr/local/libexec/brod ] && [ ! -e /usr/share/polkit-1/actions/dev.godbtw.brod.policy ] || fail "$scenario: после удаления остались файлы в /usr/local или политика"
  [ ! -e /etc/NetworkManager/conf.d/90-brod.conf ] && [ ! -e /etc/polkit-1/rules.d/50-brod-dns.rules ] || fail "$scenario: остались правила NetworkManager/polkit"
  [ ! -e /home/u/.local/opt/brod ] || fail "$scenario: остался каталог приложения"
  echo "сценарий $scenario: ок"
done
IN
docker run --rm -v "$T":/a:ro ubuntu:24.04 bash /a/run.sh
echo "установка и удаление из архива в контейнере: ок"
