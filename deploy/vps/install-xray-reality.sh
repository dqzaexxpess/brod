#!/usr/bin/env bash
# Установка собственного сервера VLESS + Reality (Xray) на VPS. Запускать на самом VPS от root:
#   curl -fsSL <ссылка на этот файл> | sudo bash        либо скопировать файл и:  sudo bash install-xray-reality.sh
# В конце печатает ссылку vless://…, которую вставляют в приложении: Настройки → Мои серверы → Сервер.
#
# Использование: sudo bash install-xray-reality.sh [--warp]   (PORT=443 SNI=www.microsoft.com NAME=brod-vps — переменные окружения)
# Что делает: ставит Xray официальным установщиком (XTLS/Xray-install), создаёт конфиг с одним пользователем на порту 443
# (Reality: сервер выдаёт себя за настоящий сайт $SNI, ключи и uuid создаются один раз и сохраняются в /usr/local/etc/xray/brod.env),
# открывает порт в ufw, если он включён, включает и запускает службу. Повторный запуск ничего не ломает: ключи те же, ссылка печатается заново.
# Не проверялся на вашей системе: прочитайте скрипт перед запуском, проверяйте на свежем VPS.
set -euo pipefail

# Параметр --warp: выход в интернет с этого VPS через Cloudflare WARP (цепочка «клиент → VPS → WARP»): сайты видят адрес Cloudflare,
# а не адрес VPS (удобно, если адрес датацентра заблокирован сервисами). Для WARP на VPS регистрируется отдельный бесплатный аккаунт
# (как это делает wgcf); ключи лежат в /usr/local/etc/xray/warp.env. Без --warp трафик выходит напрямую с VPS.
USE_WARP=0
[ "${1:-}" = "--warp" ] && USE_WARP=1

PORT=${PORT:-443}
SNI=${SNI:-www.microsoft.com}      # сайт, под который маскируется сервер: крупный, с TLS 1.3 и h2, доступный с VPS
NAME=${NAME:-brod-vps}
ENV=/usr/local/etc/xray/brod.env
CONF=/usr/local/etc/xray/config.json

[ "$(id -u)" = 0 ] || { echo "запусти от root (sudo bash …)" >&2; exit 1; }
command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl; }

if [ ! -x /usr/local/bin/xray ]; then
  bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
fi
mkdir -p "$(dirname "$ENV")"

if [ ! -f "$ENV" ]; then
  UUID=$(/usr/local/bin/xray uuid)
  KEYS=$(/usr/local/bin/xray x25519)
  PRIV=$(echo "$KEYS" | awk -F': ' '/Private/ {print $2}')
  PUB=$(echo "$KEYS" | awk -F': ' '/Public|Password/ {print $2}')
  SID=$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')
  umask 077
  printf 'UUID=%s\nPRIV=%s\nPUB=%s\nSID=%s\n' "$UUID" "$PRIV" "$PUB" "$SID" > "$ENV"
fi
# shellcheck disable=SC1090
. "$ENV"

WARP_OUT='{"protocol": "freedom", "tag": "direct"}'
ROUTING=''
if [ "$USE_WARP" = 1 ]; then
  command -v python3 >/dev/null || { apt-get update -qq && apt-get install -y -qq python3; }
  WENV=/usr/local/etc/xray/warp.env
  if [ ! -f "$WENV" ]; then
    WG=$(/usr/local/bin/xray wg)
    WPRIV=$(echo "$WG" | awk -F': ' '/Private/ {print $2}')
    WPUB=$(echo "$WG" | awk -F': ' '/Public/ {print $2}')
    RESP=$(curl -fsS -X POST https://api.cloudflareclient.com/v0a2158/reg -H 'Content-Type: application/json' -H 'CF-Client-Version: a-6.11-2158' \
      -d "{\"key\":\"$WPUB\",\"install_id\":\"\",\"fcm_token\":\"\",\"tos\":\"$(date -u +%Y-%m-%dT%H:%M:%S.000Z)\",\"model\":\"PC\",\"serial_number\":\"\",\"locale\":\"en_US\"}")
    umask 077
    echo "$RESP" | WPRIV="$WPRIV" python3 -c '
import base64, json, os, sys
d = json.load(sys.stdin)
cfg = d["config"]
peer = cfg["peers"][0]
res = list(base64.b64decode(cfg["client_id"]))
print("WPRIV=%s" % os.environ["WPRIV"])
print("WPEER=%s" % peer["public_key"])
print("WV4=%s/32" % cfg["interface"]["addresses"]["v4"])
print("WV6=%s/128" % cfg["interface"]["addresses"]["v6"])
print("WRES=%s" % ",".join(str(x) for x in res))
' > "$WENV" || { rm -f "$WENV"; echo "не удалось зарегистрировать WARP (ответ API: ${RESP:0:200})" >&2; exit 1; }
  fi
  # shellcheck disable=SC1090
  . "$WENV"
  WARP_OUT='{"protocol": "freedom", "tag": "direct"}, {"protocol": "wireguard", "tag": "warp", "settings": {"secretKey": "'"$WPRIV"'", "address": ["'"$WV4"'", "'"$WV6"'"], "peers": [{"publicKey": "'"$WPEER"'", "endpoint": "engage.cloudflareclient.com:2408"}], "reserved": ['"$WRES"'], "mtu": 1280}}'
  ROUTING=', "routing": {"domainStrategy": "AsIs", "rules": [{"type": "field", "network": "tcp,udp", "outboundTag": "warp"}]}'
fi

cat > "$CONF" <<JSON
{
  "log": {"loglevel": "warning"},
  "inbounds": [{
    "listen": "0.0.0.0", "port": $PORT, "protocol": "vless",
    "settings": {"clients": [{"id": "$UUID", "flow": "xtls-rprx-vision"}], "decryption": "none"},
    "streamSettings": {
      "network": "tcp", "security": "reality",
      "realitySettings": {"show": false, "dest": "$SNI:443", "xver": 0, "serverNames": ["$SNI"], "privateKey": "$PRIV", "shortIds": ["$SID"]}
    },
    "sniffing": {"enabled": true, "destOverride": ["http", "tls", "quic"]}
  }],
  "outbounds": [$WARP_OUT, {"protocol": "blackhole", "tag": "block"}]$ROUTING
}
JSON
chmod 600 "$CONF"
/usr/local/bin/xray run -test -config "$CONF" >/dev/null

if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then ufw allow "$PORT"/tcp >/dev/null; fi
systemctl enable xray >/dev/null 2>&1 || true
systemctl restart xray
sleep 1
systemctl is-active --quiet xray || { echo "Xray не запустился: journalctl -u xray -n 30" >&2; exit 1; }

IP=$(curl -fsS4 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')
echo
echo "Готово. Вставьте эту ссылку в приложении (Настройки → Мои серверы → Сервер):"
echo
echo "vless://$UUID@$IP:$PORT?encryption=none&flow=xtls-rprx-vision&security=reality&sni=$SNI&fp=chrome&pbk=$PUB&sid=$SID&type=tcp#$NAME"
echo
[ "$USE_WARP" = 1 ] && echo "Выход с сервера идёт через Cloudflare WARP (отдельный аккаунт, ключи в /usr/local/etc/xray/warp.env)."
echo "Порт $PORT/tcp должен быть открыт у провайдера VPS (firewall в панели). Ключи сохранены в $ENV (права 600)."
