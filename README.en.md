# Brod

> **Brod** is a free VPN/proxy client for Linux, Windows and Android built on [sing-box-lx](https://github.com/Leadaxe/sing-box-lx). It collects free proxy subscriptions into one node database, checks the nodes on your own network and connects the one you pick, optionally through a Cloudflare WARP chain. GPL-3.0. Downloads: [Releases](https://github.com/dqzaexxpess/brod/releases/latest). **The app's interface is currently in Russian only**; this README is the English documentation (the Russian one is [README.md](README.md)).

A VPN client for Linux, Windows and Android on the [sing-box-lx](https://github.com/Leadaxe/sing-box-lx) engine. It gathers free subscriptions into one node database, checks them on your network, and connects the node **you** choose: by default it never switches on its own.

Parts: the `brodd` daemon, the `brodctl` command line and a Flutter app (Russian interface). On Android the core runs inside the app.

License: GPL-3.0. Ready-made builds for Linux, Windows and Android are on the [Releases page](https://github.com/dqzaexxpess/brod/releases/latest) (first public release 0.4.0; installing on Linux from the archive is available since 0.4.1, see [Installation](#installation)).

<p align="center">
  <img src="screenshots/android-home-v3.png" width="270" alt="Home">
  <img src="screenshots/android-nodes-v2.png" width="270" alt="Nodes">
  <img src="screenshots/android-proxies-v2.png" width="270" alt="Proxies for Telegram">
</p>
<p align="center">
  <img src="screenshots/android-monitor-v2.png" width="270" alt="Monitor">
  <img src="screenshots/android-settings-v2.png" width="270" alt="Settings">
  <img src="screenshots/android-sources-v2.png" width="270" alt="Subscription sources">
</p>
<p align="center">
  <img src="screenshots/android-candidates-v2.png" width="270" alt="Discovered sources">
  <img src="screenshots/android-warp-v2.png" width="270" alt="Cloudflare WARP">
  <img src="screenshots/android-templates-v2.png" width="270" alt="Rule templates">
</p>
<p align="center">
  <img src="screenshots/linux-home-v3.png" width="330" alt="Brod on Linux">
  <img src="screenshots/windows-home-v3.png" width="320" alt="Brod on Windows">
</p>
<p align="center"><sub>Android, Linux and Windows (server addresses are blurred).</sub></p>

## Features

**Nodes**
- Fetches subscriptions in parallel, with mirrors and a cache: link lists, base64, sing-box JSON, Clash YAML and Xray JSON configs (only the nodes are taken from configs). Parses vless, vmess, trojan, ss, ssr, hysteria/hysteria2, tuic, anytls, socks and wireguard links (including AmneziaWG) and nodes with HTTP obfuscation. Understands Throne deep links (`throne://add/…`) and wg-quick configs. Removes duplicates. If a source reports subscription details (the `subscription-userinfo` header), the sources list shows the remaining traffic and the expiry date.
- Finds new sources by itself: reads the READMEs of known repositories, measures how alive their links are, merges sources with identical content. It adds nothing without you: you confirm the candidates.
- Checks nodes on the current network (results are tied to it): handshake, HTTPS, Google API reachability, targets (YouTube, Telegram, your own). Shows ping, speed, country, address cleanliness and suitability for calls ([how the labels are produced](#how-nodes-get-their-labels)).

**Choosing**
- By default the active node changes only by your command. Refreshing subscriptions, checks and a network change do not touch it. If a node stops answering, you get a notification with three freshly verified alternatives; an automatic swap can be enabled in settings.
- "Best node": scored by speed, ping, address cleanliness, YouTube and stability over the last day (share of successful checks, flapping, recent failures); before connecting, the first three candidates are verified with a real request. The node list can be sorted by freshness of the check, ping, speed, country, cleanliness and protocol.
- Targets: `youtube`, `telegram`, `google-api` and targets registered by other apps each have their own node, check and policy (`sticky-manual` or `auto-failover`).
- Your own server: `deploy/vps/install-xray-reality.sh` sets up VLESS + Reality on your VPS; paste the link into the app.

**Routing and protection**
- Modes: "Only blocked", "Everything through the exit", "Everything except RU". Rules by domain, address, port and protocol, process (Windows and Linux), app (Android), Wi-Fi name; ready-made templates; your own lists by URL.
- "Send through the exit" and "Let through directly, bypassing VPN" from the connection log: click a connection (for example, Discord voice that has no domain and went direct in "Only blocked" mode) and choose what to remember: the process, domain, address, or protocol with port. The rule goes into the rules list and is placed first; the opposite rule for the same target replaces it. There is a ready template for Discord calls.
- Kill switch: nftables on Linux, WFP filters on Windows. The rules stay if the daemon crashes and are removed from the app.
- SNI spoofing (`tls_spoof`, Linux and Windows) for networks that filter by site name; FakeIP ("Fast DNS", experimental).
- WARP: the account is created on first start (one request to Cloudflare; disable on desktop with `--no-warp-register`), a "WARP → node" chain for checks and connections, and a search for a working endpoint on your network.
- Proxy chains: "My servers" → chain icon: 2–3 nodes in a row (client → hop → … → exit). A chain works like a regular node, is checked along the whole path and is never picked by auto-selection. WireGuard may be the first hop (the other hops go through it). "Check hops" in a chain's menu shows which hop is not answering (hops over UDP cannot be checked directly).
- Sharing a proxy on the local network (Settings): other devices on your network connect to Brod as to a proxy (SOCKS5 and HTTP on one port, with a login and password).
- Android: `vless://`, `vmess://`, `trojan://` and other links open in Brod with an "add this server?" prompt; Brod can offer to add a link you copied (can be turned off in settings). If the system stops the app while the VPN is on, the service asks to be restarted, but on some firmwares (confirmed on HyperOS) it does not happen: on the next launch Brod tells you that the VPN turned off and you have to turn it on again.

**Sharing and search**
- QR code and link: the server and rule-profile menus have "Show QR code". The other device points the stock camera at it and, on Android, the link opens in Brod (a node: `vless://` etc.; a rule profile: `brod://route/…`; both ask "add?" and a profile does not switch on by itself). A server QR contains its password or key. Links that are too long are passed as text.
- Settings → search bar: finds a setting or feature by name and by everyday words and takes you to the right section.

**Other**
- First-run wizard (what a node is, the modes, a warning about free servers, how to enable automatic swap).
- Diagnostic report (Settings → Data and diagnostics): DNS, connectivity, WARP and sample-node tests, conclusions by rules, one file for troubleshooting; server addresses and node names are masked in it.
- Appearance: light, dark and system themes, six accent colors, pure-black background (AMOLED), compact node list.
- Proxies for Telegram (MTProto): a list, checks, a connect button.
- A local proxy on `127.0.0.1` with a login and password for other apps; an HTTP API and events (SSE) with tokens.
- Backup and restore, tray, notifications, in-app updates (signed, see [Security](#security)).

## How nodes get their labels

Every label in the list is the result of a separate check made through the node itself, not data from a subscription. Checks are tied to the network (interface, gateway and tunnel interfaces): on another network, for example mobile instead of Wi-Fi, a node is checked again, and its earlier history stays with the earlier network.

| Label | Where it comes from | Lifetime and color |
|---|---|---|
| **Alive** (the "Alive" filter) | a handshake and an HTTPS request (`generate_204`) through the node; names are resolved on the node's side | a success on the current network no older than 30 minutes and with no later failure |
| **Ping, ms** | the time of that same request | up to 400 ms green, up to 1200 orange, above that red |
| **Speed, Mbit/s** | a 2 MB download through the node; measured in batches of 4 nodes per round | goes stale after 6 hours; from 10 green, from 3 orange |
| **Country** (flag) | the country of the address the request leaves from (Cloudflare `cdn-cgi/trace` through the node), not the location of the node's server | 24 hours |
| **IP clean / medium / dirty** | three facts about the exit: Google search does not demand a captcha, a strict Cloudflare-protected site opens without a challenge, YouTube opens | clean — none of the checks failed, medium — some succeeded and some failed, dirty — none succeeded; no label if there were no checks; 24 hours |
| **YouTube** | video playback opens through the node (playback status); a success also counts toward IP cleanliness | collected together with the exit details |
| **for calls** | a series of 10 STUN requests (UDP) through the node to public Google and Cloudflare servers; "for calls" — loss up to 10%, median and spread up to 300 ms; "calls drop" — replies arrive but loss or spread is higher; "no UDP" — no replies. Measured in batches of 4 nodes per round, alive nodes only, the active one first | goes stale after 6 hours, tied to the network |
| **no TLS verification** | the node is configured with `allowInsecure` | permanent mark |
| **mine**, star, pin | your server, a favorite, a pinned node | set by you |

If a check fails, the node gets a failure class: `dns`, `tcp`, `tls/handshake`, `auth`, `timeout`, `freeze` (data started flowing and stopped), `target-blocked`, `bot-check`, `other`. A check that we interrupt on shutdown does not count as a failure.

How the work is scheduled. A regular round every 5 minutes: up to 200 nodes, 48 in parallel, 10 seconds per node; a recent success (15 minutes) is not re-checked, and a node that failed is tried again after 2 hours. Exit details are collected separately, 12 nodes every 3 minutes. A direct TCP check to the node's server is only a pre-filter. If checking through WARP is on, some nodes are checked through a "WARP → node" chain: some only answer that way. None of this changes your choice: a node that disappeared from its source or lost its "alive" label stays active until you change it.

The "for calls" label is separate because the other checks say nothing about it: voice and video use UDP, and HTTPS through a node can work when UDP does not pass at all. In one check of free vless nodes three of five passed no UDP at all while the others worked without loss. You can measure it by hand: `brodctl udp [node]` or "Check for calls" in a node's menu.

Calls also depend on the mode. In "Only blocked" mode, traffic that is not in the lists goes direct; Discord voice arrives on an address without a domain, and your provider may cut it. If a call drops, add the "Discord calls" template or send the connection through the exit from the connection log.

## Platforms

| | Linux | Windows 11 | Android |
|---|---|---|---|
| App | Flutter, tray, autostart | Flutter, tray, autostart | Flutter, quick-settings tile |
| Daemon | a separate process | Windows service (`%ProgramData%\Brod`) | the core inside the app |
| Tunnel | TUN (needs `cap_net_admin`) | wintun | `VpnService` |
| Kill switch | nftables | WFP filters | system "Always-on VPN" and "Block connections without VPN" |
| Per-app rules | by process name | by process name | by package, "VPN only for…" mode |
| SNI spoofing (`tls_spoof`) | yes | yes (WinDivert) | no |
| In-app update | root helper (polkit) | `install.ps1 -Update` | system installer window |
| Status | daily use | daily use | daily use |

Not verified on every platform: Wi-Fi-name conditions on Windows, operation on mobile networks with whitelists, sleep and wake on a computer. Automatic VPN recovery after the system stops the app on Android depends on the firmware (it does not work on HyperOS).

## Installation

Ready-made builds are on the [Releases page](https://github.com/dqzaexxpess/brod/releases/latest). Below is building from source; it first needs the engine: `scripts/fetch-engine.sh` downloads the right sing-box-lx version and applies the patches from `patches/`.

**Linux, from the archive** (since release 0.4.1)
```bash
mkdir brod && tar xzf brod-<version>-linux-amd64.tar.gz -C brod && cd brod && ./install.sh
```
The app, daemon and CLI go to `~/.local/opt/brod`, the commands to `~/.local/bin`, a launcher to the menu. Then one `sudo` step: the update helper (a polkit rule), the daemon in `/usr/local/lib/brod` (root:root, TUN capabilities) and the NetworkManager and polkit rules for DNS. `./install.sh --no-system` skips the sudo step (TUN mode will not work). Uninstall: `deploy/uninstall-linux.sh [--system] [--purge]`. After that updates are installed by the "Install" button in the app.

**Linux, from source**
```bash
scripts/fetch-engine.sh
deploy/install-linux.sh --build     # daemon, CLI and app into ~/.local/opt/brod, a launcher in the menu
```
TUN mode needs the right to create an interface: the script grants it in one `sudo` step. More in `deploy/README.md`. Brod does not run alongside another VPN client that brings up TUN (Throne, WireGuard, etc.).

**Windows 11**

The easiest way is to download `brod-<version>-windows-amd64-setup.exe` from the Releases page and run it (described below). From source: the app is built on Windows (Flutter); the daemon and CLI are cross-built with `scripts/make-release-windows.sh`. Installing with the script as administrator:
```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1
```
The script copies files to `%ProgramFiles%\Brod`, creates the "Brod" service and a shortcut, and adds an entry to "Settings → Apps"; state lives in `%ProgramData%\Brod`. `-User NAME` grants access to another user, `-RestrictPrivileges` reduces the service's privileges.

The installer (`setup.exe`) shows a Brod-styled window. It is a loader carrying the build archive: it opens the window, the window asks for administrator rights and runs the same `install.ps1`. Silent install: `setup.exe /S /USER=DOMAIN\Name` (exit codes: 0 success, 1 install error, 2 no such account, 3 corrupt archive, 1223 elevation refused). Uninstall — "Settings → Apps → Brod": the window asks whether to delete the node database and settings.

The Windows files are not code-signed, so SmartScreen and Defender may show a warning. If Windows says "Windows protected your PC", click "More info → Run anyway". You can verify that the file is the one published in the release by the sums in `SHA256SUMS` (`Get-FileHash file -Algorithm SHA256`).

**Android**

`scripts/build-android-local.sh` builds the core and the app (needs the Android SDK/NDK and gomobile). The build is signed with your key. By default the script takes the SDK/NDK and Gradle dependencies from mirrors (listed at the top of the script: they are not reachable directly from the author's network), while CI builds from the official sources. The app needs permission for VPN and notifications; the rest is optional (see [Android permissions](#android-permissions)).

## Quick start (command line)

```bash
scripts/build.sh                   # bin/brodd, bin/brodctl
bin/brodd &                        # the daemon, state in ~/.local/share/brod
bin/brodctl status
bin/brodctl refresh                # refresh subscriptions
bin/brodctl test                   # check nodes now
bin/brodctl list --alive           # alive on the current network
bin/brodctl connect 3fa9c1         # the active node (an ID can be shortened to 6 characters)
bin/brodctl app-proxy              # the address and login/password for apps
```

Other commands: `brodctl help` (`status`, `list`, `connect`, `fav`, `pin`, `test`, `probe`, `udp`, `speed`, `exit-info`, `alternatives`, `refresh`, `vpn`, `rules`, `profiles`, `sources`, `candidates`, `targets`, `warp`, `killswitch`, `update`, `events -f`, `logs`, `settings`, `app-proxy`, `token`; most have `--json`). Apps connect to `http://login:password@127.0.0.1:29080` (HTTP and SOCKS5 on one port). With no active node the entry is closed and traffic does not leak. The local API routes are described in `api/routes.go`.

## Security

- Secrets (node links, the entry password, tokens) live only in the state directory (permissions 0700/0600) and do not reach logs or API responses. Source addresses are shown up to the host, and the engine log is scrubbed of passwords and addresses.
- The HTTP API listens only on `127.0.0.1`, requires a token, and rejects requests with an `Origin` (a browser) or a foreign `Host`. App tokens are limited to their targets, and the rules of targets that an app registers are limited to domains; `allow_direct` for them can only be granted by the owner.
- Unencrypted nodes are hidden by default; nodes with `allowInsecure` are marked.
- Subscriptions are other people's data: links are parsed without being executed, and only nodes are taken from full configs.
- Updates are signed with an ed25519 key whose public part is built into the program: a swapped file on the host or in transit will not pass verification. Two keys are built in for the release manifest, a primary and a reserve one (in case the primary is lost); the reserve one is valid since 0.4.9. The sources list and the release manifest are kept in [brod-releases](https://github.com/dqzaexxpess/brod-releases) (mirrored on GitLab). Android builds are signed with a separate key.
- On Linux, installing updates is done by a small root helper (`cmd/brod-helper`, polkit): it accepts only signed archives.

## Android permissions

Brod asks the system only for what a specific feature needs; without the optional ones everything else works.

| Permission | Why | Required |
|---|---|---|
| Internet, network state | loading sources and checking nodes; telling Wi-Fi from mobile (the key of the check history, a warning before a big download on mobile) | yes |
| VPN (the system "Connection request" window) | the tunnel itself through `VpnService`; without it the VPN cannot be turned on | yes |
| Foreground service and notification | while the VPN is on, the system must not kill the service; the notification shows the mode, protocol, country and a "Disconnect" button | yes |
| Notifications (Android 13+) | to show that notification; if refused, the VPN works but the notification is not visible | no |
| Do not restrict battery | so the system does not put the service to sleep in the background | no (recommended) |
| Install apps | installing a downloaded and verified Brod update (the system confirmation window remains) | no, only for in-app updating |
| App list (package visibility) | choosing apps for "VPN only for…" and rules by package name; Telegram for `tg://` links | no, only for per-app rules |
| Wi-Fi state and "always" location | **only** routing rules by Wi-Fi network name (`wifi_ssid`, `wifi_bssid`): Android gives the network name only to apps with this permission, and the VPN service reads it in the background | no: without it only the by-network-name condition does not work |

Brod does not use location: no GPS, no coordinates, no cell towers; the permission is needed only because Android hides the Wi-Fi name without it. If you have no rules by network name, you do not need to grant it: it is requested only by the "Allow" button in such a rule's window. The app contains no analytics and sends nothing to the developer.

## Development

```bash
source scripts/env.sh
TAGS=$(cat scripts/tags.txt)
CGO_ENABLED=0 go vet -tags "$TAGS" ./... && CGO_ENABLED=0 go test -tags "$TAGS" ./...      # ~1.5 min
CGO_ENABLED=1 go test -race -tags "$TAGS" ./core/ ./api/ ./ext/...                          # races
cd app && flutter analyze && flutter test
```

`scripts/ci-local.sh` runs the same as CI (`go`, `flutter`, `security` or all at once). The acceptance tests (`core/acceptance_test.go`) use a real Xray from `bin/xray` (`scripts/fetch-xray.sh`); without it they are skipped.

| package | purpose |
|---|---|
| `core` | the daemon core: selection, refresh, check scheduler, health, targets, events |
| `engine` | sing-box-lx as a library: probe and persistent nodes, endpoints, switches |
| `ext/{switch,dispatch,ssr,rawhttp}` | engine plugins (switchable target, domain dispatch, ShadowsocksR, HTTP obfuscation) |
| `store` | SQLite: nodes, sources, network-tied checks, targets, selection, the journal |
| `prober`, `monitor` | L1–L4 checks and a passive freeze detector by area |
| `aggregator`, `translator`, `importer`, `discover` | loading subscriptions, parsing links and configs, discovering new sources |
| `api`, `cli`, `notify`, `cmd/{brodd,brodctl,brod-helper}` | interfaces: the API, the command line, notifications, binaries |
| `supervisor`, `killswitch`, `netwatch` | turning the VPN on, the kill switch, watching the network |
| `warp`, `mobile`, `rulesets` | WARP, mobile whitelists, rule sets |
| `updater`, `ota` | checking, signing and installing updates |
| `androidcore`, `app/` | the Android core (gomobile) and the Flutter app |
| `patches/` | engine patches: `patches/lx` (sing-box-lx) and `patches/sing` |

## Reporting a problem

Bugs and suggestions go to the issues of this repository. Please include the platform, the version and the log (`brodctl logs`; in the Android app — the "Monitor" tab, "Share log"). Passwords and addresses are scrubbed from the log automatically, but check before sending.

## Credits

[sing-box](https://github.com/SagerNet/sing-box) and the fork [sing-box-lx](https://github.com/Leadaxe/sing-box-lx), [wintun](https://www.wintun.net), [WinDivert](https://reqrypt.org/windivert.html), [Xray-core](https://github.com/XTLS/Xray-core) (for tests), [Flutter](https://flutter.dev).
