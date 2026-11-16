# Установка и системные настройки

Всё здесь **готовится, но не применяется автоматически**: режим TUN и службы требуют прав администратора.

## Без sudo: пользовательская служба
```bash
scripts/build.sh && deploy/install-user.sh --enable
journalctl --user -u brodd -f      # лог демона (без секретов)
brodctl status
```
Юнит `brodd.user.service` не включает песочницы с пространствами имён (на Ubuntu 25.04 их ограничивает AppArmor) — только
`NoNewPrivileges`, `RestrictAddressFamilies`, `LockPersonality`. Остановка: `systemctl --user stop brodd` (демон закрывает сокет и базу ≤15 с).

## С sudo: режим TUN
Решение (2026-11-03): **обычный пользовательский `brodd` с правами на бинарнике**, а не системная служба (одно состояние, уведомления и API как сейчас; для друга на Windows/Android Linux-служба не нужна — там другие механизмы). `deploy/brodd-tun.service` оставлен как заготовка на потом.

Разово, в терминале: **`sudo deploy/tun-setup.sh`** — (1) `setcap` на `bin/brodd` (повторять после каждой пересборки), (2) правило NetworkManager (интерфейс `brod0` не под его управлением), (3) правило polkit: ваш пользователь может менять DNS на интерфейсах через systemd-resolved (только 4 действия resolve1, локальная активная сессия; убрать — `sudo rm /etc/polkit-1/rules.d/50-vpnclient-dns.rules`). После этого `brodd --tun [--tun-mode blocked|all|all-but-ru]` сам направляет DNS системы в TUN (`resolvectl` на интерфейсе, при остановке сбрасывает); отключить — `--tun-no-dns`. Если права нет, демон пишет событие `error` («DNS системы не направлен в TUN») — тогда настройте вручную:
   ```bash
   resolvectl dns brod0 10.77.77.2      # соседний адрес подсети TUN
   resolvectl domain brod0 '~.'
   ```
IPv6 по умолчанию блокируется внутри TUN. Включается `--tun-allow-ipv6`.

## Проверка TUN по шагам (рекомендуемый порядок)
1. `sudo deploy/tun-setup.sh` (или только `sudo deploy/tun-setcap.sh`) — право создавать TUN без root (после **каждой** пересборки `bin/brodd`; снять: `sudo setcap -r bin/brodd`).
2. Безопасная проверка конвейера при включённом Throne: `brodd --tun --tun-no-route` (интерфейс создаётся, системные маршруты не меняются).
Адрес TUN по умолчанию `10.77.77.1/30` выбран так, чтобы не пересекаться с Throne (`172.19.0.0/24`) и Docker (`172.17.0.0/16`). DNS системы направляется в TUN через `resolvectl dns brod0 10.77.77.2` + `~.` — без этого запросы `systemd-resolved` уходят на роутер (приватный адрес → напрямую) и это утечка.

## Откат
```bash
systemctl --user disable --now brodd; rm ~/.config/systemd/user/brodd.service ~/.local/bin/brodd ~/.local/bin/brodctl
# состояние (узлы, выбор, токены): ~/.local/share/brod — удалять только если выбор и токены не нужны
```
