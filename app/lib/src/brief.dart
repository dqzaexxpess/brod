import 'api.dart';
import 'country.dart';

/// Названия протоколов для подписей (на Android те же в BrodVpnService.kt).
const _protoNames = <String, String>{
  'vless': 'VLESS',
  'vmess': 'VMess',
  'trojan': 'Trojan',
  'ss': 'Shadowsocks',
  'shadowsocks': 'Shadowsocks',
  'hysteria2': 'Hysteria2',
  'hy2': 'Hysteria2',
  'tuic': 'TUIC',
  'wireguard': 'WireGuard',
  'warp': 'WARP',
  'chain': 'Цепочка',
};

String protoName(String p) => _protoNames[p.toLowerCase()] ?? p.toUpperCase();

/// Строка «Режим · Протокол · 🇳🇱 Страна» для трея и уведомлений (как у службы на Android); части без данных пропускаются, пусто — нечего показывать.
/// flag: false — страна без эмодзи-флага: меню и подсказку трея рисует сама среда рабочего стола, вшитый шрифт с флагами ей недоступен,
/// и вместо флага были бы пустые квадраты.
String briefLine(VpnState? v, ActiveNode? a, {bool flag = true}) {
  final parts = <String>[];
  final mode = v == null ? null : vpnModes[v.mode];
  if (mode != null) parts.add(mode);
  if (a != null && a.proto.isNotEmpty) parts.add(protoName(a.proto));
  if (a != null && a.country.length == 2 && a.country != 'XX') parts.add(flag ? '${flagOf(a.country)} ${countryName(a.country)}' : countryName(a.country));
  return parts.join(' · ');
}
