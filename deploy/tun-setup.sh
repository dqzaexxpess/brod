#!/usr/bin/env bash
# Разовая настройка системы для режима TUN. ЗАПУСКАТЬ ВАМ, в обычном терминале:  sudo deploy/tun-setup.sh [путь-к-brodd]
# Путь по умолчанию — bin/brodd (разработка); после установки deploy/install-linux.sh права выдаёт он сам для ~/.local/opt/brod/brodd.
# (повторять нужно только setcap — после каждой пересборки/обновления brodd; запуск скрипта целиком безопасен)
#
# Что делает (и больше ничего):
#  1. Право на бинарник: brodd получает cap_net_admin и cap_net_bind_service (создавать TUN и маршруты без root) и cap_net_raw (сырые сокеты для подмены SNI, tls_spoof).
#  2. NetworkManager: интерфейс brod0 (и прежний vpnclient0) не под его управлением (иначе он сбросит адреса и маршруты).
#  3. polkit: ваш пользователь может менять DNS/домены/маршрут по умолчанию на интерфейсах через systemd-resolved
#     (только эти действия resolve1, только локальная активная сессия). Так демон сам направляет DNS системы в TUN.
#     Это правило — единственное, что расширяет права ваших программ; убрать: sudo rm /etc/polkit-1/rules.d/50-brod-dns.rules
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "запусти через sudo: sudo deploy/tun-setup.sh" >&2; exit 1; }
cd "$(dirname "$0")/.."
U=${SUDO_USER:?нужен запуск через sudo из-под обычного пользователя}
VPND=${1:-bin/brodd}
[ "$(basename "$VPND")" = brodd ] || { echo "ожидается файл с именем brodd (право cap_net_admin выдаётся только ему): $VPND" >&2; exit 1; }
[ -x "$VPND" ] || { echo "нет $VPND — scripts/build.sh (или укажите путь к brodd)" >&2; exit 1; }

setcap cap_net_admin,cap_net_bind_service,cap_net_raw+ep "$VPND"
getcap "$VPND"

# NetworkManager и polkit есть не везде (серверы, systemd-networkd, минимальные установки): без них эти шаги не нужны и не должны обрывать установку
if [ -d /etc/NetworkManager/conf.d ]; then
  install -m 0644 deploy/NetworkManager-brod.conf /etc/NetworkManager/conf.d/90-brod.conf
  rm -f /etc/NetworkManager/conf.d/90-vpnclient.conf # прежнее имя файла: теперь правило в 90-brod.conf
  systemctl reload NetworkManager 2>/dev/null || true # не запущен или нет systemd — не ошибка
else
  echo "NetworkManager не найден: правило «интерфейс brod0 не под его управлением» пропущено"
fi
if [ ! -d /etc/polkit-1/rules.d ]; then
  echo "нет /etc/polkit-1/rules.d (polkit не установлен): DNS системы в TUN направить не получится, остальное готово"
  echo "готово: setcap"
  exit 0
fi

cat > /etc/polkit-1/rules.d/50-brod-dns.rules <<RULES
// Brod: демон пользователя $U сам направляет DNS системы в свой TUN-интерфейс (resolvectl).
polkit.addRule(function(action, subject) {
  var ok = ["set-dns-servers", "set-domains", "set-default-route", "revert"];
  if (subject.user == "$U" && subject.local && subject.active && action.id.indexOf("org.freedesktop.resolve1.") == 0 &&
      ok.indexOf(action.id.substring("org.freedesktop.resolve1.".length)) >= 0) {
    return polkit.Result.YES;
  }
});
RULES
chmod 0644 /etc/polkit-1/rules.d/50-brod-dns.rules
rm -f /etc/polkit-1/rules.d/50-vpnclient-dns.rules # прежнее имя файла
echo "готово: setcap, NetworkManager (если есть), polkit"
