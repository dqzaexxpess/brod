import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'api_access.dart';
import 'platform_paths.dart';
import 'win_notices.dart';

/// Режимы VPN (совпадают с `brodctl vpn mode`).
const vpnModes = <String, String>{
  'blocked': 'Только заблокированное',
  'all': 'Всё через выход',
  'all-but-ru': 'Всё, кроме РФ',
};

/// Состояние переключателя (`GET /v1/vpn`).
class VpnState {
  const VpnState(
      {required this.on, required this.mode, this.iface = '', this.error = '', this.killswitch = '', this.killswitchError = '', this.conflict = const [], this.autoconnect = ''});

  final bool on;
  final String mode;
  final String iface;
  final String error;
  final String killswitch; // active — правила файрвола стоят; failed — нужны, но не поставились; пусто — не нужны
  final String killswitchError;
  final List<String> conflict; // активные чужие туннели (throne-tun…): с ними Brod, как правило, не подключается
  final String autoconnect; // включать ли VPN при запуске демона: last | always | off; пусто — решает система (Android)

  @override
  bool operator ==(Object other) =>
      other is VpnState &&
      other.on == on &&
      other.mode == mode &&
      other.iface == iface &&
      other.error == error &&
      other.killswitch == killswitch &&
      other.killswitchError == killswitchError &&
      other.conflict.join(',') == conflict.join(',') &&
      other.autoconnect == autoconnect;

  @override
  int get hashCode => Object.hash(on, mode, iface, error, killswitch, killswitchError, conflict.join(','), autoconnect);

  VpnState withMode(String m) =>
      VpnState(on: on, mode: m, iface: iface, error: error, killswitch: killswitch, killswitchError: killswitchError, conflict: conflict, autoconnect: autoconnect);

  factory VpnState.fromJson(Map<String, dynamic> j) => VpnState(
        on: j['on'] == true,
        mode: (j['mode'] as String?) ?? 'blocked',
        iface: (j['interface'] as String?) ?? '',
        error: (j['error'] as String?) ?? '',
        killswitch: (j['killswitch'] as String?) ?? '',
        killswitchError: (j['killswitch_error'] as String?) ?? '',
        conflict: [for (final c in (j['conflict'] as List?) ?? const []) c.toString()],
        autoconnect: (j['autoconnect'] as String?) ?? '',
      );
}

/// Условия правила маршрутизации (пустое — не проверяется; разные виды — «И», значения внутри — «ИЛИ»).
class RouteMatch {
  RouteMatch({
    List<String>? domain,
    List<String>? domainSuffix,
    List<String>? domainKeyword,
    List<String>? domainRegex,
    List<String>? ipCidr,
    List<int>? port,
    List<String>? portRange,
    this.network = '',
    List<String>? processName,
    List<String>? ruleSet,
    this.ipVersion = 0,
    List<String>? protocol,
    List<String>? sourceIpCidr,
    this.ipIsPrivate = false,
    this.sourceIpIsPrivate = false,
    List<int>? sourcePort,
    List<String>? sourcePortRange,
    List<String>? processPath,
    List<String>? processPathRegex,
    List<String>? packageName,
    List<String>? wifiSsid,
    List<String>? wifiBssid,
    Map<String, dynamic>? extra,
    this.invert = false,
  })  : domain = domain ?? [],
        domainSuffix = domainSuffix ?? [],
        domainKeyword = domainKeyword ?? [],
        domainRegex = domainRegex ?? [],
        ipCidr = ipCidr ?? [],
        port = port ?? [],
        portRange = portRange ?? [],
        processName = processName ?? [],
        ruleSet = ruleSet ?? [],
        protocol = protocol ?? [],
        sourceIpCidr = sourceIpCidr ?? [],
        sourcePort = sourcePort ?? [],
        sourcePortRange = sourcePortRange ?? [],
        processPath = processPath ?? [],
        processPathRegex = processPathRegex ?? [],
        packageName = packageName ?? [],
        wifiSsid = wifiSsid ?? [],
        wifiBssid = wifiBssid ?? [],
        extra = extra ?? {};

  List<String> domain, domainSuffix, domainKeyword, domainRegex, ipCidr, portRange, processName, ruleSet;
  List<int> port;
  String network;
  // условия «как в Throne»
  int ipVersion; // 0 — любая, 4 или 6
  List<String> protocol, sourceIpCidr, sourcePortRange, processPath, processPathRegex, packageName, wifiSsid, wifiBssid;
  // условия, которых этот экран не знает (добавило более новое ядро или CLI): хранятся как есть и уходят обратно при сохранении — иначе правка
  // правила молча теряла бы условие, и правило начинало бы совпадать с более широким кругом трафика
  Map<String, dynamic> extra;
  List<int> sourcePort;
  bool ipIsPrivate, sourceIpIsPrivate, invert;

  static List<String> _s(Object? v) => [for (final e in (v as List? ?? const [])) '$e'];
  static List<int> _i(Object? v) => [for (final e in (v as List? ?? const [])) (e as num).toInt()];

  factory RouteMatch.fromJson(Map<String, dynamic> j) => RouteMatch(
        domain: _s(j['domain']),
        domainSuffix: _s(j['domain_suffix']),
        domainKeyword: _s(j['domain_keyword']),
        domainRegex: _s(j['domain_regex']),
        ipCidr: _s(j['ip_cidr']),
        port: _i(j['port']),
        portRange: _s(j['port_range']),
        network: (j['network'] as String?) ?? '',
        processName: _s(j['process_name']),
        ruleSet: _s(j['rule_set']),
        ipVersion: (j['ip_version'] as num?)?.toInt() ?? 0,
        protocol: _s(j['protocol']),
        sourceIpCidr: _s(j['source_ip_cidr']),
        ipIsPrivate: j['ip_is_private'] == true,
        sourceIpIsPrivate: j['source_ip_is_private'] == true,
        sourcePort: _i(j['source_port']),
        sourcePortRange: _s(j['source_port_range']),
        processPath: _s(j['process_path']),
        processPathRegex: _s(j['process_path_regex']),
        packageName: _s(j['package_name']),
        wifiSsid: _s(j['wifi_ssid']),
        wifiBssid: _s(j['wifi_bssid']),
        extra: {for (final e in j.entries) if (!_known.contains(e.key)) e.key: e.value},
        invert: j['invert'] == true,
      );

  static const _known = {
    'domain', 'domain_suffix', 'domain_keyword', 'domain_regex', 'ip_cidr', 'port', 'port_range', 'network', 'process_name', 'rule_set', 'ip_version', 'protocol',
    'source_ip_cidr', 'ip_is_private', 'source_ip_is_private', 'source_port', 'source_port_range', 'process_path', 'process_path_regex', 'package_name',
    'wifi_ssid', 'wifi_bssid', 'invert',
  };

  Map<String, dynamic> toJson() => {
        if (domain.isNotEmpty) 'domain': domain,
        if (domainSuffix.isNotEmpty) 'domain_suffix': domainSuffix,
        if (domainKeyword.isNotEmpty) 'domain_keyword': domainKeyword,
        if (domainRegex.isNotEmpty) 'domain_regex': domainRegex,
        if (ipCidr.isNotEmpty) 'ip_cidr': ipCidr,
        if (port.isNotEmpty) 'port': port,
        if (portRange.isNotEmpty) 'port_range': portRange,
        if (network.isNotEmpty) 'network': network,
        if (processName.isNotEmpty) 'process_name': processName,
        if (ruleSet.isNotEmpty) 'rule_set': ruleSet,
        if (ipVersion != 0) 'ip_version': ipVersion,
        if (protocol.isNotEmpty) 'protocol': protocol,
        if (sourceIpCidr.isNotEmpty) 'source_ip_cidr': sourceIpCidr,
        if (ipIsPrivate) 'ip_is_private': true,
        if (sourceIpIsPrivate) 'source_ip_is_private': true,
        if (sourcePort.isNotEmpty) 'source_port': sourcePort,
        if (sourcePortRange.isNotEmpty) 'source_port_range': sourcePortRange,
        if (processPath.isNotEmpty) 'process_path': processPath,
        if (processPathRegex.isNotEmpty) 'process_path_regex': processPathRegex,
        if (packageName.isNotEmpty) 'package_name': packageName,
        if (wifiSsid.isNotEmpty) 'wifi_ssid': wifiSsid,
        if (wifiBssid.isNotEmpty) 'wifi_bssid': wifiBssid,
        ...extra,
        if (invert) 'invert': true,
      };

  RouteMatch copy() => RouteMatch.fromJson(toJson());

  /// Нет ни одного условия (флаг «инвертировать» сам по себе условием не считается).
  bool get isEmpty => (Map.of(toJson())..remove('invert')).isEmpty;

  /// Краткое описание для списка.
  String get summary {
    final parts = <String>[
      if (domainSuffix.isNotEmpty) 'сайты: ${domainSuffix.take(3).join(', ')}${domainSuffix.length > 3 ? ' …' : ''}',
      if (domain.isNotEmpty) 'домены: ${domain.take(2).join(', ')}${domain.length > 2 ? ' …' : ''}',
      if (domainKeyword.isNotEmpty) 'слова: ${domainKeyword.take(3).join(', ')}',
      if (domainRegex.isNotEmpty) 'regex: ${domainRegex.length}',
      if (ipCidr.isNotEmpty) 'IP: ${ipCidr.take(2).join(', ')}${ipCidr.length > 2 ? ' …' : ''}',
      if (ipIsPrivate) 'локальные адреса',
      if (sourceIpCidr.isNotEmpty) 'отправитель: ${sourceIpCidr.take(2).join(', ')}${sourceIpCidr.length > 2 ? ' …' : ''}',
      if (sourceIpIsPrivate) 'отправитель локальный',
      if (ipVersion != 0) 'IPv$ipVersion',
      if (protocol.isNotEmpty) 'протокол: ${protocol.take(3).join(', ')}',
      if (port.isNotEmpty || portRange.isNotEmpty) 'порты: ${[...port.map((e) => '$e'), ...portRange].take(4).join(', ')}',
      if (sourcePort.isNotEmpty || sourcePortRange.isNotEmpty) 'порты отправителя: ${[...sourcePort.map((e) => '$e'), ...sourcePortRange].take(3).join(', ')}',
      if (processName.isNotEmpty) 'процессы: ${processName.take(3).join(', ')}',
      if (processPath.isNotEmpty || processPathRegex.isNotEmpty) 'пути процессов: ${processPath.length + processPathRegex.length}',
      if (packageName.isNotEmpty) 'пакеты: ${packageName.take(2).join(', ')}${packageName.length > 2 ? ' …' : ''}',
      if (wifiSsid.isNotEmpty) 'Wi-Fi: ${wifiSsid.take(2).join(', ')}${wifiSsid.length > 2 ? ' …' : ''}',
      if (wifiBssid.isNotEmpty) 'точки доступа: ${wifiBssid.length}',
      if (extra.isNotEmpty) 'другие условия: ${extra.length}',
      if (ruleSet.isNotEmpty) 'списки: ${ruleSet.length}',
      if (network.isNotEmpty) network.toUpperCase(),
    ];
    final s = parts.join(' · ');
    return invert ? 'НЕ ($s)' : s;
  }
}

/// Правило маршрутизации.
/// Выход правила при действии proxy: конкретный узел, страна (лучший живой узел в ней, с автозаменой) или WARP; пусто — общий диспетчер.
class RouteViaDto {
  RouteViaDto({this.kind = '', this.node = '', this.country = '', this.fallback = ''});

  String kind; // node | country | warp
  String node;
  String country; // ISO alpha-2
  String fallback; // что делать, когда подходящего выхода нет: block (по умолчанию) | proxy | direct

  factory RouteViaDto.fromJson(Map<String, dynamic> j) => RouteViaDto(
        kind: (j['kind'] as String?) ?? '',
        node: (j['node'] as String?) ?? '',
        country: (j['country'] as String?) ?? '',
        fallback: (j['fallback'] as String?) ?? '',
      );

  Map<String, dynamic> toJson() => {
        'kind': kind,
        if (node.isNotEmpty && kind == 'node') 'node': node,
        if (country.isNotEmpty && kind == 'country') 'country': country,
        if (fallback.isNotEmpty) 'fallback': fallback,
      };
}

/// Страна, в которой есть проверенные узлы (`countries` в `GET /v1/route-rules`).
class CountryDto {
  const CountryDto(this.code, this.alive, this.total);

  final String code;
  final int alive;
  final int total;
}

/// Текущая привязка выхода правила (`via` в `GET /v1/route-rules`).
class ViaStateDto {
  const ViaStateDto({required this.rule, this.target = '', this.node = '', this.fallback = false, this.candidates = 0});

  final String rule;
  final String target; // node | warp | active | direct | none (соединения отклоняются)
  final String node;
  final bool fallback; // подходящего выхода нет, действует запасной вариант
  final int candidates;
}

class RouteRuleDto {
  RouteRuleDto({this.id = '', this.title = '', this.enabled = true, this.action = 'proxy', RouteMatch? match, this.via, this.blockMethod = '', this.tlsFragment = false, this.tlsRecordFragment = false, this.tlsSpoof = '', this.tlsSpoofMethod = ''}) : match = match ?? RouteMatch();

  String id;
  String title;
  bool enabled;
  String action; // proxy | direct | block
  RouteMatch match;
  RouteViaDto? via; // только при action = proxy
  String blockMethod; // только при block: '' (отказ клиенту) | drop (молча отбросить)
  bool tlsFragment, tlsRecordFragment; // дробление TLS ClientHello (обход DPI), для proxy и direct
  String tlsSpoof, tlsSpoofMethod; // подмена SNI (Linux и Windows): имя сайта для ложного ClientHello и способ порчи пакета ('' — по умолчанию)

  factory RouteRuleDto.fromJson(Map<String, dynamic> j) => RouteRuleDto(
        id: (j['id'] as String?) ?? '',
        title: (j['title'] as String?) ?? '',
        enabled: j['enabled'] != false,
        action: (j['action'] as String?) ?? 'proxy',
        match: RouteMatch.fromJson((j['match'] as Map<String, dynamic>?) ?? {}),
        via: j['via'] is Map<String, dynamic> ? RouteViaDto.fromJson(j['via'] as Map<String, dynamic>) : null,
        blockMethod: (j['block_method'] as String?) ?? '',
        tlsFragment: j['tls_fragment'] == true,
        tlsRecordFragment: j['tls_record_fragment'] == true,
        tlsSpoof: (j['tls_spoof'] as String?) ?? '',
        tlsSpoofMethod: (j['tls_spoof_method'] as String?) ?? '',
      );

  Map<String, dynamic> toJson() => {
        'id': id,
        'title': title,
        'enabled': enabled,
        'action': action,
        'match': match.toJson(),
        if (via != null && via!.kind.isNotEmpty && action == 'proxy') 'via': via!.toJson(),
        if (blockMethod.isNotEmpty && action == 'block') 'block_method': blockMethod,
        if (tlsFragment && action != 'block') 'tls_fragment': true,
        if (tlsRecordFragment && action != 'block') 'tls_record_fragment': true,
        if (tlsSpoof.isNotEmpty && action != 'block') 'tls_spoof': tlsSpoof,
        if (tlsSpoof.isNotEmpty && tlsSpoofMethod.isNotEmpty && action != 'block') 'tls_spoof_method': tlsSpoofMethod,
      };

  RouteRuleDto copy() => RouteRuleDto.fromJson(toJson());
}

/// Профиль маршрутизации (`GET /v1/route-profiles`).
class RouteProfileDto {
  const RouteProfileDto({required this.id, required this.name, this.rules = 0, this.active = false});

  final String id;
  final String name;
  final int rules;
  final bool active;

  factory RouteProfileDto.fromJson(Map<String, dynamic> j) =>
      RouteProfileDto(id: (j['id'] as String?) ?? '', name: (j['name'] as String?) ?? '', rules: (j['rules'] as num?)?.toInt() ?? 0, active: j['active'] == true);
}

/// Свой список маршрутизации по URL (`GET /v1/route-sets`).
class UserSetDto {
  const UserSetDto({required this.id, this.title = '', this.url = '', this.updated, this.size = 0, this.error = ''});

  final String id; // ext-… : так на него ссылаются правила
  final String title;
  final String url;
  final DateTime? updated;
  final int size;
  final String error; // последнее обновление не удалось (файл остаётся прежним)

  String get label => title.isNotEmpty ? title : Uri.tryParse(url)?.host ?? id;

  factory UserSetDto.fromJson(Map<String, dynamic> j) => UserSetDto(
        id: (j['id'] as String?) ?? '',
        title: (j['title'] as String?) ?? '',
        url: (j['url'] as String?) ?? '',
        updated: j['updated'] is String ? DateTime.tryParse(j['updated'] as String) : null,
        size: (j['size'] as num?)?.toInt() ?? 0,
        error: (j['error'] as String?) ?? '',
      );
}

/// Ответ `GET /v1/route-rules`.
class RouteRulesDto {
  const RouteRulesDto(this.rules, this.sets, {this.countries = const [], this.via = const {}});

  final List<RouteRuleDto> rules;
  final List<String> sets; // имена встроенных списков
  final List<CountryDto> countries; // страны с проверенными узлами
  final Map<String, ViaStateDto> via; // привязки выходов по id правила
}

/// Источник подписки из `GET /v1/sources`.
class SourceDto {
  const SourceDto({required this.name, required this.host, required this.enabled, required this.userAdded, this.lastOk, this.lastError = '', this.lastCount = 0, this.title = '', this.usedBytes = 0, this.totalBytes = 0, this.expire, this.present = 0, this.checked = 0, this.alive = 0, this.rarely = false});

  final String name;
  final String host;
  final bool enabled;
  final bool userAdded;
  final DateTime? lastOk;
  final String lastError;
  final int lastCount;
  final String title;
  final int usedBytes; // израсходовано по данным источника
  final int totalBytes; // квота (0 — не сообщается)
  final DateTime? expire; // когда подписка истекает
  final int present, checked, alive; // отдача за неделю: узлов в источнике, проверено, ответило хотя бы раз
  final bool rarely; // за неделю ни один узел не ответил: автоматически обновляется не чаще раза в сутки

  factory SourceDto.fromJson(Map<String, dynamic> j) => SourceDto(
        name: (j['name'] as String?) ?? '',
        host: (j['host'] as String?) ?? '',
        enabled: j['enabled'] == true,
        userAdded: j['user_added'] == true,
        lastOk: j['last_ok'] is String ? DateTime.tryParse(j['last_ok'] as String) : null,
        lastError: (j['last_error'] as String?) ?? '',
        lastCount: (j['last_count'] as num?)?.toInt() ?? 0,
        title: (j['title'] as String?) ?? '',
        usedBytes: (j['used_bytes'] as num?)?.toInt() ?? 0,
        totalBytes: (j['total_bytes'] as num?)?.toInt() ?? 0,
        expire: j['expire'] is String ? DateTime.tryParse(j['expire'] as String) : null,
        present: (j['present'] as num?)?.toInt() ?? 0,
        checked: (j['checked'] as num?)?.toInt() ?? 0,
        alive: (j['alive'] as num?)?.toInt() ?? 0,
        rarely: j['rarely'] == true,
      );
}

/// Кандидат в источники, найденный автопоиском (`GET /v1/source-candidates`): лежит отдельно от источников, включает его владелец.
class CandidateDto {
  const CandidateDto({required this.id, required this.name, this.repo = '', this.url = '', this.links = 0, this.schemes = const {}, this.daysSeen = 0, this.sampled = 0, this.sampleOk = 0, this.sampleDays = 0, this.group = '', this.groupSize = 1});

  final String id;
  final String name;
  final String repo;
  final String url;
  final int links; // ссылок узлов в последней загрузке
  final Map<String, int> schemes; // схема -> число ссылок
  final int daysSeen; // дней наблюдения
  final int sampled; // проверено ссылок из выборок (накопительно)
  final int sampleOk; // из проверенных ответило
  final int sampleDays; // дней, в которые брали выборку
  final String group; // группа похожих по содержимому кандидатов (зеркал); пусто — старый демон
  final int groupSize;

  factory CandidateDto.fromJson(Map<String, dynamic> j) => CandidateDto(
        id: (j['id'] as String?) ?? '',
        name: (j['name'] as String?) ?? '',
        repo: (j['repo'] as String?) ?? '',
        url: (j['url'] as String?) ?? '',
        links: (j['links'] as num?)?.toInt() ?? 0,
        schemes: {for (final e in ((j['schemes'] as Map?) ?? const {}).entries) '${e.key}': (e.value as num?)?.toInt() ?? 0},
        daysSeen: (j['days_seen'] as num?)?.toInt() ?? 0,
        sampled: (j['sampled'] as num?)?.toInt() ?? 0,
        sampleOk: (j['sample_ok'] as num?)?.toInt() ?? 0,
        sampleDays: (j['sample_days'] as num?)?.toInt() ?? 0,
        group: (j['group'] as String?) ?? '',
        groupSize: (j['group_size'] as num?)?.toInt() ?? 1,
      );
}

/// Группа кандидатов-зеркал (`GET /v1/source-candidate-groups`): представитель (самый живой) и остальные члены.
class CandidateGroupDto {
  const CandidateGroupDto({required this.id, required this.representative, this.members = const [], this.size = 1, this.totalLinksMax = 0});

  final String id;
  final CandidateDto representative;
  final List<CandidateDto> members; // без представителя
  final int size;
  final int totalLinksMax; // наибольшее число ссылок среди членов

  factory CandidateGroupDto.fromJson(Map<String, dynamic> j) {
    final members = [for (final e in ((j['members'] as List?) ?? const [])) CandidateDto.fromJson(e as Map<String, dynamic>)];
    return CandidateGroupDto(
      id: (j['id'] as String?) ?? '',
      representative: CandidateDto.fromJson((j['representative'] as Map).cast<String, dynamic>()),
      members: members,
      size: (j['size'] as num?)?.toInt() ?? members.length + 1,
      totalLinksMax: (j['total_links_max'] as num?)?.toInt() ?? 0,
    );
  }
}

/// Отсчёт скорости за секунду (`GET /v1/traffic`).
class TrafficSample {
  const TrafficSample(this.t, this.up, this.down);

  final int t; // unix-секунды
  final int up; // байт за секунду
  final int down;
}

class TrafficStats {
  const TrafficStats({this.samples = const [], this.upNow = 0, this.downNow = 0, this.upTotal = 0, this.downTotal = 0, this.conns = 0});

  final List<TrafficSample> samples;
  final int upNow;
  final int downNow;
  final int upTotal;
  final int downTotal;
  final int conns;

  factory TrafficStats.fromJson(Map<String, dynamic> j) => TrafficStats(
        samples: [
          for (final e in (j['samples'] as List? ?? const []))
            TrafficSample((e['t'] as num).toInt(), (e['up'] as num).toInt(), (e['down'] as num).toInt()),
        ],
        upNow: (j['up_now'] as num?)?.toInt() ?? 0,
        downNow: (j['down_now'] as num?)?.toInt() ?? 0,
        upTotal: (j['up_total'] as num?)?.toInt() ?? 0,
        downTotal: (j['down_total'] as num?)?.toInt() ?? 0,
        conns: (j['conns'] as num?)?.toInt() ?? 0,
      );
}

/// Соединение (`GET /v1/connections`).
class ConnDto {
  const ConnDto({
    required this.id,
    required this.network,
    required this.dest,
    required this.route,
    required this.node,
    this.inbound = '',
    this.source = '',
    this.process = '',
    this.routeName = '',
    this.up = 0,
    this.down = 0,
    required this.started,
    this.closed,
  });

  final String id;
  final String network;
  final String dest;
  final String route; // direct | proxy | target | block | other
  final String node;
  final String inbound;
  final String source;
  final String process;
  final String routeName;
  final int up;
  final int down;
  final DateTime started;
  final DateTime? closed;

  bool get open => closed == null;

  factory ConnDto.fromJson(Map<String, dynamic> j) => ConnDto(
        id: (j['id'] as String?) ?? '',
        network: (j['network'] as String?) ?? '',
        dest: (j['dest'] as String?) ?? '',
        route: (j['route'] as String?) ?? 'other',
        node: (j['node'] as String?) ?? '',
        inbound: (j['inbound'] as String?) ?? '',
        source: (j['source'] as String?) ?? '',
        process: (j['process'] as String?) ?? '',
        routeName: (j['route_name'] as String?) ?? '',
        up: (j['up'] as num?)?.toInt() ?? 0,
        down: (j['down'] as num?)?.toInt() ?? 0,
        started: DateTime.tryParse((j['started'] as String?) ?? '') ?? DateTime.now(),
        closed: j['closed'] is String ? DateTime.tryParse(j['closed'] as String) : null,
      );
}

/// Событие журнала (`GET /v1/events`).
class EventDto {
  const EventDto({required this.id, required this.at, required this.kind, this.initiator = '', this.detail = ''});

  final int id;
  final DateTime at;
  final String kind;
  final String initiator;
  final String detail;

  factory EventDto.fromJson(Map<String, dynamic> j) {
    final d = j['detail'];
    return EventDto(
      id: (j['id'] as num?)?.toInt() ?? 0,
      at: DateTime.tryParse((j['at'] as String?) ?? '') ?? DateTime.now(),
      kind: (j['kind'] as String?) ?? '',
      initiator: (j['initiator'] as String?) ?? '',
      detail: d == null ? '' : (d is String ? d : jsonEncode(d)),
    );
  }
}

/// Трафик за день (`GET /v1/traffic/daily`).
class DayTrafficDto {
  const DayTrafficDto(this.day, this.up, this.down, this.vpnUp, this.vpnDown);

  final String day; // YYYY-MM-DD
  final int up;
  final int down;
  final int vpnUp;
  final int vpnDown;

  int get total => up + down;
  int get vpnTotal => vpnUp + vpnDown;
}

/// Вывод диагностики.
class DiagFindingDto {
  const DiagFindingDto(this.level, this.title, this.detail);

  final String level; // ok | warn | bad | info
  final String title;
  final String detail;
}

/// Итог диагностики «что видят сайты» (`POST /v1/diagnostics`).
class DiagDto {
  const DiagDto({this.ip = '', this.country = '', this.colo = '', this.warp = false, this.ipv6 = '', this.resolver = '', this.exitIp = '', this.vpnOn = false, this.mode = '', this.resolverCountry = '', this.geo = const [], this.findings = const []});

  final String ip;
  final String country;
  final String colo;
  final bool warp;
  final String ipv6;
  final String resolver;
  final String exitIp;
  final bool vpnOn;
  final String mode;
  final String resolverCountry;
  final List<GeoDto> geo; // ответы независимых баз геолокации
  final List<DiagFindingDto> findings;

  factory DiagDto.fromJson(Map<String, dynamic> j) => DiagDto(
        ip: (j['ip'] as String?) ?? '',
        country: (j['country'] as String?) ?? '',
        colo: (j['colo'] as String?) ?? '',
        warp: j['warp'] == true,
        ipv6: (j['ipv6'] as String?) ?? '',
        resolver: (j['resolver'] as String?) ?? '',
        exitIp: (j['exit_ip'] as String?) ?? '',
        vpnOn: j['vpn_on'] == true,
        mode: (j['mode'] as String?) ?? '',
        resolverCountry: (j['resolver_country'] as String?) ?? '',
        geo: [for (final g in (j['geo'] as List? ?? const [])) GeoDto.fromJson(g as Map<String, dynamic>)],
        findings: [
          for (final f in (j['findings'] as List? ?? const [])) DiagFindingDto((f['level'] as String?) ?? 'info', (f['title'] as String?) ?? '', (f['detail'] as String?) ?? ''),
        ],
      );
}

/// Ответ одной базы геолокации о внешнем адресе (в `geo` диагностики).
class GeoDto {
  const GeoDto({required this.name, this.country = '', this.city = '', this.timezone = '', this.org = '', this.hosting, this.proxy, this.error = ''});

  final String name;
  final String country;
  final String city;
  final String timezone;
  final String org;
  final bool? hosting; // база считает адрес дата-центром
  final bool? proxy; // база считает адрес прокси или VPN
  final String error;

  factory GeoDto.fromJson(Map<String, dynamic> j) => GeoDto(
        name: (j['name'] as String?) ?? '',
        country: (j['country'] as String?) ?? '',
        city: (j['city'] as String?) ?? '',
        timezone: (j['timezone'] as String?) ?? '',
        org: (j['org'] as String?) ?? '',
        hosting: j['hosting'] as bool?,
        proxy: j['proxy'] as bool?,
        error: (j['error'] as String?) ?? '',
      );
}

/// Ход пакетного замера (`GET /v1/measure`).
class MeasureDto {
  const MeasureDto({this.running = false, this.total = 0, this.done = 0, this.stage = '', this.ok = 0, this.canceled = false});

  final bool running;
  final int total;
  final int done;
  final String stage; // ping | exit | speed
  final int ok;
  final bool canceled;

  double get fraction => total == 0 ? 0 : done / total;

  String get stageName => switch (stage) { 'ping' => 'пинг', 'exit' => 'страна и чистота', 'speed' => 'скорость', _ => '' };

  factory MeasureDto.fromJson(Map<String, dynamic> j) => MeasureDto(
        running: j['running'] == true,
        total: (j['total'] as num?)?.toInt() ?? 0,
        done: (j['done'] as num?)?.toInt() ?? 0,
        stage: (j['stage'] as String?) ?? '',
        ok: (j['ok'] as num?)?.toInt() ?? 0,
        canceled: j['canceled'] == true,
      );
}

/// Что видят сайты на выходе узла (`exit` в `GET /v1/nodes`).
class ExitDto {
  const ExitDto({required this.ip, required this.country, this.colo = '', this.googleOk, this.siteOk, this.clean = 'unknown', this.detail = ''});

  final String ip;
  final String country; // ISO alpha-2
  final String colo;
  final bool? googleOk;
  final bool? siteOk;
  final String clean; // clean | mixed | dirty | unknown
  final String detail;

  factory ExitDto.fromJson(Map<String, dynamic> j) => ExitDto(
        ip: (j['ip'] as String?) ?? '',
        country: (j['country'] as String?) ?? '',
        colo: (j['colo'] as String?) ?? '',
        googleOk: j['google_ok'] as bool?,
        siteOk: j['site_ok'] as bool?,
        clean: (j['clean'] as String?) ?? 'unknown',
        detail: (j['detail'] as String?) ?? '',
      );
}

/// Узел из `GET /v1/nodes`.
class NodeDto {
  const NodeDto({
    required this.id,
    required this.proto,
    required this.server,
    required this.port,
    this.favorite = false,
    this.pinned = false,
    this.insecure = false,
    this.l2At,
    this.googleApi = false,
    this.youtube = false,
    this.notes = const [],
    this.exit,
    this.latencyMs = 0,
    this.speedKBps = 0,
    this.udp = '',
    this.udpMs = 0,
    this.own = false,
    this.label = '',
  });

  final String id;
  final String proto;
  final String server;
  final int port;
  final bool favorite;
  final bool pinned;
  final bool insecure;
  final DateTime? l2At; // последний успех проверки в текущей сети
  final bool googleApi;
  final bool youtube;
  final List<String> notes;
  final ExitDto? exit;
  final bool own; // собственный сервер пользователя
  final String label; // пользовательское название ('' — нет)
  final int latencyMs; // пинг: задержка последней успешной проверки, мс (0 — нет данных)
  final double speedKBps; // скорость скачивания, КБ/с (0 — не замерялась)
  final String udp; // замер UDP в текущей сети: good | unstable | none; '' — нет свежего замера (годен ли узел для звонков)
  final int udpMs; // медиана задержки STUN-ответов, мс

  /// Скорость в Мбит/с.
  double get speedMbps => speedKBps * 8 / 1024;

  /// Строка для сравнения при обновлении списка: если ничего не изменилось, перерисовывать нечего.
  String get signature => '$id|${l2At?.millisecondsSinceEpoch}|$favorite|$pinned|$own|$label|$youtube|$googleApi|${exit?.country}|${exit?.clean}|$latencyMs|${speedKBps.round()}|$udp';

  bool get alive => l2At != null;
  String get address => port > 0 ? '$server:$port' : server;
  String get title => label.isNotEmpty ? label : address;

  factory NodeDto.fromJson(Map<String, dynamic> j) => NodeDto(
        id: (j['id'] as String?) ?? '',
        proto: (j['proto'] as String?) ?? '',
        server: (j['server'] as String?) ?? '',
        port: (j['port'] as num?)?.toInt() ?? 0,
        favorite: j['favorite'] == true,
        pinned: j['pinned'] == true,
        insecure: j['insecure'] == true,
        l2At: j['l2_at'] is String ? DateTime.tryParse(j['l2_at'] as String) : null,
        googleApi: j['google_api'] == true,
        youtube: j['youtube'] == true,
        notes: [for (final n in (j['notes'] as List? ?? const [])) '$n'],
        exit: j['exit'] is Map<String, dynamic> ? ExitDto.fromJson(j['exit'] as Map<String, dynamic>) : null,
        latencyMs: (j['latency_ms'] as num?)?.toInt() ?? 0,
        speedKBps: (j['speed_kbps'] as num?)?.toDouble() ?? 0,
        udp: (j['udp'] as String?) ?? '',
        udpMs: (j['udp_ms'] as num?)?.toInt() ?? 0,
        own: j['own'] == true,
        label: (j['label'] as String?) ?? '',
      );
}

/// Активный узел из `GET /v1/status`.
class ActiveNode {
  const ActiveNode({required this.proto, required this.server, required this.port, this.id = '', this.country = '', this.clean = '', this.latencyMs = 0, this.speedKBps = 0, this.udp = '', this.youtube = false, this.insecure = false, this.own = false, this.ip = '', this.label = ''});

  final String id;
  final String proto;
  final String server;
  final int port;
  final String country; // ISO-код страны выхода ('' — ещё не определена)
  final String clean; // clean | mixed | dirty | unknown | ''
  final int latencyMs;
  final double speedKBps;
  final String udp; // good | unstable | none | ''
  final bool youtube; // YouTube через узел открывается
  final bool insecure; // узел без проверки TLS
  final bool own; // собственный сервер
  final String ip;
  final String label;

  @override
  bool operator ==(Object other) => other is ActiveNode && other.id == id && other.proto == proto && other.server == server && other.port == port && other.country == country && other.clean == clean && other.latencyMs == latencyMs && other.speedKBps.round() == speedKBps.round() && other.udp == udp && other.youtube == youtube && other.insecure == insecure && other.own == own && other.ip == ip && other.label == label;

  @override
  int get hashCode => Object.hash(id, proto, server, port, country, clean, latencyMs, speedKBps.round(), udp, youtube, insecure, own, ip, label);

  String get title => proto == 'warp' ? 'Cloudflare WARP' : (label.isNotEmpty ? label : (port > 0 ? '$proto · $server:$port' : '$proto · $server'));
}

/// Что демон делает сейчас: обновление подписок, круги проверки узлов и прокси Telegram (для индикации загрузки).
class Activity {
  const Activity({this.refreshing = false, this.checking = false, this.checkPicked = 0, this.checkDone = 0, this.checkOk = 0, this.tgChecking = false, this.tgPicked = 0, this.tgDone = 0, this.tgOk = 0});

  final bool refreshing;
  final bool checking;
  final int checkPicked;
  final int checkDone;
  final int checkOk;
  final bool tgChecking;
  final int tgPicked;
  final int tgDone;
  final int tgOk;

  bool get busy => refreshing || checking;

  factory Activity.fromJson(Object? j) {
    if (j is! Map) return const Activity();
    int n(String k) => (j[k] as num?)?.toInt() ?? 0;
    return Activity(
      refreshing: j['refreshing'] == true,
      checking: j['checking'] == true,
      checkPicked: n('check_picked'),
      checkDone: n('check_done'),
      checkOk: n('check_ok'),
      tgChecking: j['tg_checking'] == true,
      tgPicked: n('tg_picked'),
      tgDone: n('tg_done'),
      tgOk: n('tg_ok'),
    );
  }

  @override
  bool operator ==(Object other) =>
      other is Activity &&
      other.refreshing == refreshing &&
      other.checking == checking &&
      other.checkPicked == checkPicked &&
      other.checkDone == checkDone &&
      other.checkOk == checkOk &&
      other.tgChecking == tgChecking &&
      other.tgPicked == tgPicked &&
      other.tgDone == tgDone &&
      other.tgOk == tgOk;

  @override
  int get hashCode => Object.hash(refreshing, checking, checkPicked, checkDone, checkOk, tgChecking, tgPicked, tgDone, tgOk);
}

/// Сводка демона (`GET /v1/status`).
/// Скачивание файла обновления (`download` в `GET /v1/update`).
class UpdateDownloadDto {
  const UpdateDownloadDto({this.state = 'none', this.bytes = 0, this.total = 0, this.path = '', this.error = ''});

  final String state; // none | downloading | ready | failed
  final int bytes;
  final int total;
  final String path; // готовый проверенный файл
  final String error;

  bool get downloading => state == 'downloading';
  bool get ready => state == 'ready';
  bool get failed => state == 'failed';

  factory UpdateDownloadDto.fromJson(Map<String, dynamic> j) => UpdateDownloadDto(
        state: (j['state'] as String?) ?? 'none',
        bytes: (j['bytes'] as num?)?.toInt() ?? 0,
        total: (j['total'] as num?)?.toInt() ?? 0,
        path: (j['path'] as String?) ?? '',
        error: (j['error'] as String?) ?? '',
      );
}

/// Состояние обновления приложения (`GET /v1/update`).
class UpdateDto {
  const UpdateDto({this.current = '', this.platform = '', this.checked, this.error = '', this.available = false, this.app = '', this.notes = '', this.size = 0, this.sha256 = '', this.download = const UpdateDownloadDto(), this.metered = false, this.installing = false, this.last});

  final String current;
  final String platform;
  final DateTime? checked; // null — проверка ещё не выполнялась
  final String error; // последняя проверка не удалась
  final bool available;
  final String app; // версия в манифесте
  final String notes;
  final int size;
  final String sha256;
  final UpdateDownloadDto download;
  final bool metered; // сеть платная (мобильная): файл качается только с явного согласия
  final bool installing; // установка запущена: демон сейчас завершится
  final UpdateResultDto? last; // итог последней установки (виден после перезапуска)

  factory UpdateDto.fromJson(Map<String, dynamic> j) => UpdateDto(
        current: (j['current'] as String?) ?? '',
        platform: (j['platform'] as String?) ?? '',
        checked: j['checked'] is String ? DateTime.tryParse(j['checked'] as String) : null,
        error: (j['error'] as String?) ?? '',
        available: j['available'] == true,
        app: (j['app'] as String?) ?? '',
        notes: (j['notes'] as String?) ?? '',
        size: (j['size'] as num?)?.toInt() ?? 0,
        sha256: (j['sha256'] as String?) ?? '',
        download: j['download'] is Map<String, dynamic> ? UpdateDownloadDto.fromJson(j['download'] as Map<String, dynamic>) : const UpdateDownloadDto(),
        metered: j['metered'] == true,
        installing: j['installing'] == true,
        last: j['last'] is Map<String, dynamic> ? UpdateResultDto.fromJson(j['last'] as Map<String, dynamic>) : null,
      );
}

/// Итог установки обновления (Linux, Windows): записывает помощник, ядро показывает после перезапуска.
class UpdateResultDto {
  const UpdateResultDto({this.from = '', this.to = '', this.ok = false, this.error = '', this.at});

  final String from;
  final String to;
  final bool ok;
  final String error;
  final DateTime? at;

  factory UpdateResultDto.fromJson(Map<String, dynamic> j) => UpdateResultDto(
        from: (j['from'] as String?) ?? '',
        to: (j['to'] as String?) ?? '',
        ok: j['ok'] == true,
        error: (j['error'] as String?) ?? '',
        at: j['at'] is String ? DateTime.tryParse(j['at'] as String) : null,
      );
}

/// Краткая сводка об обновлении в статусе (есть, только когда доступна новая версия): для плашки на главном экране.
class UpdateBrief {
  const UpdateBrief({required this.app, this.download = 'none'});

  final String app;
  final String download;

  @override
  bool operator ==(Object other) => other is UpdateBrief && other.app == app && other.download == download;

  @override
  int get hashCode => Object.hash(app, download);
}

class DaemonStatus {
  const DaemonStatus({required this.version, required this.nodes, required this.present, required this.degraded, this.active, this.warpEnabled = false, this.activity = const Activity(), this.update, this.netMode = '', this.vpn});

  final String version;
  final int nodes;
  final int present;
  final bool degraded;
  final ActiveNode? active;
  final bool warpEnabled;
  final Activity activity;
  final UpdateBrief? update; // есть новая версия
  final String netMode; // режим мобильной сети: open, whitelist, offline; пусто — не определён
  final VpnState? vpn; // состояние VPN из того же ответа (старый демон не присылает); в сравнение не входит, его сравнивает опрос сам

  @override
  bool operator ==(Object other) =>
      other is DaemonStatus &&
      other.netMode == netMode &&
      other.update == update &&
      other.version == version &&
      other.nodes == nodes &&
      other.present == present &&
      other.degraded == degraded &&
      other.active == active &&
      other.warpEnabled == warpEnabled &&
      other.activity == activity;

  @override
  int get hashCode => Object.hash(version, nodes, present, degraded, active, warpEnabled, activity, update, netMode);

  factory DaemonStatus.fromJson(Map<String, dynamic> j) {
    final a = j['active'];
    final d = j['degraded'];
    final w = j['warp'];
    return DaemonStatus(
      version: (j['version'] as String?) ?? '',
      nodes: (j['nodes'] as num?)?.toInt() ?? 0,
      present: (j['present'] as num?)?.toInt() ?? 0,
      degraded: d == true || (d is Map && d.isNotEmpty),
      warpEnabled: w is Map && w['enabled'] == true,
      netMode: (j['net_mode'] as String?) ?? '',
      vpn: j['vpn'] is Map<String, dynamic> ? VpnState.fromJson(j['vpn'] as Map<String, dynamic>) : null,
      activity: Activity.fromJson(j['activity']),
      update: j['update'] is Map && (j['update'] as Map)['available'] == true
          ? UpdateBrief(app: ((j['update'] as Map)['app'] as String?) ?? '', download: ((j['update'] as Map)['download'] as String?) ?? 'none')
          : null,
      active: a is Map
          ? ActiveNode(
              id: (a['id'] as String?) ?? '',
              proto: (a['proto'] as String?) ?? '',
              server: (a['server'] as String?) ?? '',
              port: (a['port'] as num?)?.toInt() ?? 0,
              country: a['exit'] is Map ? ((a['exit'] as Map)['country'] as String? ?? '') : '',
              clean: a['exit'] is Map ? ((a['exit'] as Map)['clean'] as String? ?? '') : '',
              ip: a['exit'] is Map ? ((a['exit'] as Map)['ip'] as String? ?? '') : '',
              label: (a['label'] as String?) ?? '',
              latencyMs: (a['latency_ms'] as num?)?.toInt() ?? 0,
              speedKBps: (a['speed_kbps'] as num?)?.toDouble() ?? 0,
              udp: (a['udp'] as String?) ?? '',
              youtube: a['youtube'] == true,
              insecure: a['insecure'] == true,
              own: a['own'] == true,
            )
          : null,
    );
  }
}

/// Результат активной проверки выхода (`POST /v1/probe-active`).
class ProbeResult {
  const ProbeResult({required this.ok, this.latencyMs = 0, this.detail = ''});

  final bool ok;
  final int latencyMs;
  final String detail;

  factory ProbeResult.fromJson(Map<String, dynamic> j) => ProbeResult(
        ok: j['ok'] == true,
        latencyMs: (j['latency_ms'] as num?)?.toInt() ?? 0,
        detail: [j['class'], j['detail']].where((e) => e != null && '$e'.isNotEmpty).join(' '),
      );
}

class ApiException implements Exception {
  ApiException(this.status, this.message);

  final int status;
  final String message;

  @override
  String toString() => message;
}

/// Интерфейс к демону; в тестах подменяется.
/// Профиль встроенного WARP: адрес, порт и маскировка (то, чем сети отличаются друг от друга).
class WarpProfileDto {
  const WarpProfileDto({required this.endpoint, required this.port, required this.mask, this.mtu = 0});

  final String endpoint;
  final int port;
  final String mask; // quic | dns | stun | sip | none
  final int mtu;

  factory WarpProfileDto.fromJson(Map<String, dynamic> j) =>
      WarpProfileDto(endpoint: (j['endpoint'] as String?) ?? '', port: (j['port'] as num?)?.toInt() ?? 0, mask: (j['mask'] as String?) ?? '', mtu: (j['mtu'] as num?)?.toInt() ?? 0);

  @override
  String toString() => '$endpoint:$port · ${mask == 'none' ? 'без маскировки' : mask}';

  @override
  bool operator ==(Object other) => other is WarpProfileDto && other.endpoint == endpoint && other.port == port && other.mask == mask && other.mtu == mtu;

  @override
  int get hashCode => Object.hash(endpoint, port, mask, mtu);
}

/// Состояние окна «Cloudflare WARP».
class WarpStateDto {
  const WarpStateDto({this.enabled = false, this.auto = true, this.scanning = false, this.down = false, this.current, this.remembered, this.manual = const {}, this.addrV4 = ''});

  final bool enabled; // аккаунт настроен
  final bool auto; // профиль подбирается под сеть сам
  final bool scanning; // идёт подбор
  final bool down; // WARP не отвечает, цепочка временно отключена
  final WarpProfileDto? current;
  final WarpProfileDto? remembered; // профиль, запомненный для текущей сети
  final Map<String, String> manual; // заданные вручную параметры (ключи warp.*)
  final String addrV4;

  factory WarpStateDto.fromJson(Map<String, dynamic> j) => WarpStateDto(
        enabled: j['enabled'] == true,
        auto: j['auto'] != false,
        scanning: j['scanning'] == true,
        down: j['down'] == true,
        current: j['current'] is Map<String, dynamic> ? WarpProfileDto.fromJson(j['current'] as Map<String, dynamic>) : null,
        remembered: j['remembered'] is Map<String, dynamic> ? WarpProfileDto.fromJson(j['remembered'] as Map<String, dynamic>) : null,
        manual: {for (final e in ((j['manual'] as Map?) ?? const {}).entries) '${e.key}': '${e.value}'},
        addrV4: (j['addr_v4'] as String?) ?? '',
      );
}

/// Итог пробы одного профиля при подборе.
class WarpScanResultDto {
  const WarpScanResultDto({required this.profile, this.ok = false, this.latencyMs = 0, this.cls = ''});

  final WarpProfileDto profile;
  final bool ok;
  final int latencyMs;
  final String cls;

  factory WarpScanResultDto.fromJson(Map<String, dynamic> j) => WarpScanResultDto(
        profile: WarpProfileDto.fromJson((j['profile'] as Map<String, dynamic>?) ?? const {}),
        ok: j['ok'] == true,
        latencyMs: (j['latency_ms'] as num?)?.toInt() ?? 0,
        cls: (j['class'] as String?) ?? '',
      );
}

abstract class DaemonApi {
  Future<VpnState> vpn();
  Future<VpnState> setVpn({bool? on, String? mode, String? autoconnect});
  Future<DaemonStatus> status();
  Future<ProbeResult> probe({bool quick = false}); // quick — лёгкая проверка «выход поднялся» (два круга вместо пяти)
  Future<List<NodeDto>> nodes({bool alive = false, bool favorites = false, int limit = 300, String source = ''});
  Future<String> addOwn(String text); // человекочитаемый итог
  Future<void> removeOwn(String id);
  Future<String> nodeLink(String id); // ссылка «поделиться» (с секретами)
  Future<void> connectNode(String id);
  Future<void> setFavorite(String id, bool on);
  // прокси для Telegram (вкладка «Прокси»)
  Future<TgProxyList> tgProxies({bool alive = false, bool fav = false, String kind = '', int limit = 300});
  Future<int> tgAdd(String text); // сколько добавлено
  Future<String> tgCheck(); // человекочитаемый итог круга проверки
  Future<void> tgDelete(String id);
  Future<void> tgFavorite(String id, bool on);
  Future<ExitDto> exitInfo(String id);
  Future<double> measureSpeed(String id); // КБ/с

  /// Замер UDP через узел (годен ли для звонков): оценка good | unstable | none и медиана задержки, мс.
  Future<({String grade, int medianMs, int lossPct})> probeUdp(String id) async => throw ApiException(501, 'замер UDP не поддерживается этим ядром');
  Future<MeasureDto> startMeasure(List<String> ids, {bool ping = false, bool exit = false, bool speed = false});
  Future<MeasureDto> measureState();
  Future<NodeDto> connectBest();
  Future<List<DayTrafficDto>> dailyTraffic({int days = 30});
  Future<DiagDto> diagnostics();
  Future<Map<String, dynamic>> backupExport();
  Future<String> backupImport(Map<String, dynamic> doc);
  Future<String> editOwn(String id, {String? label, String? server, int? port, String? link}); // возвращает id (при замене ссылкой он меняется)
  Future<void> setLabel(String id, String label);
  Future<TrafficStats> traffic({int n = 120});
  Future<List<ConnDto>> connections({bool closed = false, int limit = 300});
  Future<void> closeConnection(String id);
  Future<List<String>> engineLog({int n = 300});
  Future<List<EventDto>> events({int limit = 200});
  Future<MeasureDto> cancelMeasure();
  Future<RouteRulesDto> routeRules();
  Future<List<UserSetDto>> userSets();
  Future<UserSetDto> addUserSet(String url, String title); // скачивается и проверяется сразу
  Future<void> removeUserSet(String id);
  Future<String?> putRouteRules(List<RouteRuleDto> rules); // null — применено; иначе текст ошибки перезапуска
  Future<List<RouteProfileDto>> routeProfiles();
  Future<RouteProfileDto> createRouteProfile(String name, {String from = ''});
  Future<RouteProfileDto> importRouteProfile(String text, {String name = ''}); // ссылка brod://route/… или JSON
  Future<void> renameRouteProfile(String id, String name);
  Future<void> deleteRouteProfile(String id);
  Future<String?> activateRouteProfile(String id); // null — применено; иначе текст ошибки перезапуска VPN
  Future<String> shareRouteProfile(String id); // ссылка brod://route/…
  Future<List<SourceDto>> sources();
  Future<void> addSource(String name, String url);
  Future<void> setSourceEnabled(String name, bool on);
  Future<void> removeSource(String name);
  // автопоиск источников: кандидаты лежат отдельно, заводит их только владелец
  Future<List<CandidateDto>> sourceCandidates();
  Future<String> addCandidate(String id); // имя созданного источника
  Future<void> dismissCandidate(String id);
  Future<List<CandidateGroupDto>> candidateGroups(); // кандидаты, сгруппированные по содержимому (старый демон: 404)
  Future<void> dismissCandidateGroup(String id); // скрыть всех членов группы
  Future<String> scanCandidates({bool allowMetered = false}); // внеочередной проход (до нескольких минут); человекочитаемый итог
  Future<Map<String, String>> settings();
  Future<void> putSetting(String key, String value);
  Future<String> updateRuleSets();
  Future<String> setTunApps(String mode, List<String> apps); // раздельное туннелирование (Android) одной записью; текст ошибки перезапуска VPN или пусто
  Future<String> otaSources(); // забирает подписанный список источников у издателя; текст итога
  Future<UpdateDto> update(); // состояние обновления приложения
  Future<UpdateDto> checkUpdate(); // проверить сейчас (причина неудачи — в UpdateDto.error)
  Future<UpdateDto> installUpdate({int appPid = 0}); // Linux, Windows: проверить, распаковать, запустить помощника; демон завершится
  Future<UpdateDto> downloadUpdate({bool allowMetered = false}); // скачать файл своей платформы и проверить по манифесту (ход — в update())
  Future<WarpStateDto> warpState(); // окно «Cloudflare WARP»: аккаунт, действующий и запомненный для сети профиль
  Future<List<WarpScanResultDto>> warpScan({bool apply = true, int stopAtGood = 0}); // подбор профиля под текущую сеть (до нескольких минут)
  Future<void> warpParams(Map<String, String> params); // ручные параметры (ключи warp.*) одной записью; ядро пересоздаётся один раз
  Future<void> warpReset(); // вернуть умолчания
  Future<String> warpRegister({bool replace = false}); // создаёт аккаунт WARP (напрямую или через активный узел), ядро перезапускается
  Future<String> warpImport(Map<String, dynamic> account, {bool replace = false}); // готовый warp-account.json
  Future<String> refreshSources();
  Future<String> checkNow();
  void close();
}

/// Метка версии ответа для условного запроса: [sent] уходит в If-None-Match, [got] — ETag ответа, [same] — демон ответил 304.
class _Tag {
  _Tag(this.sent);
  final String? sent;
  String? got;
  bool same = false;
}

/// HTTP по unix-сокету демона (права 0600, вызывающий — владелец, токен не нужен).
class UnixApi implements DaemonApi {
  UnixApi(this.socketPath, {this.readTimeout = const Duration(seconds: 20)}) {
    _client = HttpClient()
      ..connectionTimeout = const Duration(seconds: 3)
      ..connectionFactory = (uri, proxyHost, proxyPort) =>
          Socket.startConnect(InternetAddress(socketPath, type: InternetAddressType.unix), 0);
  }

  final String socketPath;
  final Duration readTimeout; // таймаут обычного запроса (долгие операции передают свой)
  late final HttpClient _client;

  /// Путь сокета по тем же правилам, что у демона (`paths.Socket`).
  static String defaultSocket() => socketPathFor(Platform.environment, windows: Platform.isWindows);

  Future<Map<String, dynamic>> _call(String method, String path, [Object? body, Duration? timeout]) async {
    final d = await _callAny(method, path, body, timeout ?? readTimeout);
    return d is Map<String, dynamic> ? d : <String, dynamic>{};
  }

  Future<Object?> _callAny(String method, String path, [Object? body, Duration? timeout, _Tag? tag]) async {
    timeout ??= readTimeout;
    // ядро пересоздаётся при смене режима/настроек: запрос на чтение, попавший на закрывающуюся базу, безопасно повторить
    for (var attempt = 0;; attempt++) {
      try {
        return await _callOnce(method, path, body, timeout, tag);
      } on ApiException catch (e) {
        if (method != 'GET' || attempt >= 3 || !(e.status == 503 || e.message.contains('database is closed'))) rethrow;
        await Future<void>.delayed(const Duration(milliseconds: 500));
      }
    }
  }

  Future<Object?> _callOnce(String method, String path, Object? body, Duration timeout, _Tag? tag) async {
    final fb = _fallback;
    if (fb != null) return _exchange(_plain, Uri.parse('${fb.base}$path'), method, body, timeout, token: fb.token, tag: tag);
    try {
      return await _exchange(_client, Uri.parse('http://brodd$path'), method, body, timeout, tag: tag);
    } on SocketException catch (e) {
      // Windows: демон запущен задачей с наивысшими правами, а приложение — с обычными, и сокет ему не открывается; тогда loopback-HTTP с токеном
      if (!socketAccessDenied(e.osError?.errorCode, windows: Platform.isWindows)) rethrow;
      final acc = _readAccess();
      if (acc == null) rethrow;
      _fallback = acc;
      return _exchange(_plain, Uri.parse('${acc.base}$path'), method, body, timeout, token: acc.token, tag: tag);
    }
  }

  ApiAccess? _fallback;
  final HttpClient _plain = HttpClient()..connectionTimeout = const Duration(seconds: 3);

  ApiAccess? _readAccess() {
    try {
      final dir = File(socketPath).parent.path; // brodd.sock лежит в каталоге состояния
      return parseApiAccess(File('$dir${Platform.pathSeparator}api-access.json').readAsStringSync());
    } catch (_) {
      return null;
    }
  }

  Future<Object?> _exchange(HttpClient client, Uri uri, String method, Object? body, Duration timeout, {String? token, _Tag? tag}) async {
    final req = await client.openUrl(method, uri);
    if (tag?.sent != null) req.headers.set(HttpHeaders.ifNoneMatchHeader, tag!.sent!);
    if (token != null) req.headers.set(HttpHeaders.authorizationHeader, 'Bearer $token');
    if (body != null) {
      req.headers.contentType = ContentType.json;
      req.add(utf8.encode(jsonEncode(body)));
    }
    // по таймауту запрос отменяется: иначе соединение живёт до ответа демона и при зависшем демоне копятся по одному на опрос
    final HttpClientResponse resp;
    try {
      resp = await req.close().timeout(timeout);
    } on TimeoutException {
      req.abort();
      rethrow;
    }
    if (tag != null) {
      tag.got = resp.headers.value(HttpHeaders.etagHeader);
      if (resp.statusCode == HttpStatus.notModified) {
        tag.same = true;
        await resp.drain<void>();
        return null;
      }
    }
    final String text;
    try {
      text = await resp.transform(utf8.decoder).join().timeout(timeout);
    } on TimeoutException {
      req.abort();
      rethrow;
    }
    Object? json;
    try {
      json = jsonDecode(text);
    } catch (_) {}
    if (resp.statusCode >= 400) {
      throw ApiException(resp.statusCode, (json is Map ? json['error'] as String? : null) ?? 'HTTP ${resp.statusCode}');
    }
    return json;
  }

  @override
  Future<VpnState> vpn() async => VpnState.fromJson(await _call('GET', '/v1/vpn'));

  @override
  Future<VpnState> setVpn({bool? on, String? mode, String? autoconnect}) async =>
      VpnState.fromJson(await _call('POST', '/v1/vpn', {'on': ?on, 'mode': ?mode, 'autoconnect': ?autoconnect}, const Duration(seconds: 60)));

  @override
  Future<DaemonStatus> status() async => DaemonStatus.fromJson(await _call('GET', '/v1/status'));

  @override
  Future<ProbeResult> probe({bool quick = false}) async => ProbeResult.fromJson(await _call('POST', quick ? '/v1/probe-active?quick=1' : '/v1/probe-active', null, const Duration(seconds: 30)));

  @override
  Future<List<NodeDto>> nodes({bool alive = false, bool favorites = false, int limit = 300, String source = ''}) async {
    final path = '/v1/nodes?limit=$limit${alive ? '&alive=1' : ''}${favorites ? '&favorites=1' : ''}${source.isEmpty ? '' : '&source=$source'}';
    // метка версии списка: не изменился — демон отвечает 304 без тела, разбор полумегабайта JSON пропускается
    final cached = _nodesCache[path];
    final tag = _Tag(cached?.etag);
    final r = await _callAny('GET', path, null, null, tag);
    if (tag.same && cached != null) return cached.list;
    final list = [for (final e in (r as List)) NodeDto.fromJson(e as Map<String, dynamic>)];
    if (tag.got != null) {
      if (_nodesCache.length > 8) _nodesCache.clear();
      _nodesCache[path] = (etag: tag.got!, list: list);
    } else {
      _nodesCache.remove(path);
    }
    return list;
  }

  final Map<String, ({String etag, List<NodeDto> list})> _nodesCache = {};

  @override
  Future<void> connectNode(String id) async => _call('POST', '/v1/nodes/$id/connect', null, const Duration(seconds: 60));

  /// Просит демон снять правила киллсвитча, оставшиеся после его падения (нужен работающий демон; при включённом VPN демон откажет).
  Future<void> releaseKillswitch() async => _call('POST', '/v1/killswitch/release', null, const Duration(seconds: 20));

  @override
  Future<void> setFavorite(String id, bool on) async => _call('POST', '/v1/nodes/$id/favorite', {'on': on});

  @override
  Future<TgProxyList> tgProxies({bool alive = false, bool fav = false, String kind = '', int limit = 300}) async =>
      TgProxyList.fromJson(await _call('GET', '/v1/tg-proxies?limit=$limit${alive ? '&alive=1' : ''}${fav ? '&fav=1' : ''}${kind.isEmpty ? '' : '&kind=$kind'}'));

  @override
  Future<int> tgAdd(String text) async => ((await _call('POST', '/v1/tg-proxies', {'text': text}))['added'] as num?)?.toInt() ?? 0;

  @override
  Future<String> tgCheck() async {
    final r = await _call('POST', '/v1/tg-proxies/check', null, const Duration(minutes: 3));
    return 'Проверено ${r['picked'] ?? 0}, живых ${r['ok'] ?? 0}';
  }

  @override
  Future<void> tgDelete(String id) async => _call('DELETE', '/v1/tg-proxies/$id');

  @override
  Future<void> tgFavorite(String id, bool on) async => _call('PUT', '/v1/tg-proxies/$id/favorite', {'on': on});

  @override
  Future<ExitDto> exitInfo(String id) async {
    final r = await _call('POST', '/v1/nodes/$id/exit-info', null, const Duration(seconds: 90));
    return ExitDto.fromJson((r['exit'] as Map<String, dynamic>?) ?? {});
  }

  @override
  Future<double> measureSpeed(String id) async {
    final r = await _call('POST', '/v1/nodes/$id/speed', null, const Duration(seconds: 90));
    return (r['speed_kbps'] as num?)?.toDouble() ?? 0;
  }

  @override
  Future<({String grade, int medianMs, int lossPct})> probeUdp(String id) async {
    final r = await _call('POST', '/v1/nodes/$id/udp', null, const Duration(seconds: 40));
    return (grade: (r['grade'] as String?) ?? 'none', medianMs: (r['median_ms'] as num?)?.toInt() ?? 0, lossPct: (r['loss_pct'] as num?)?.round() ?? 100);
  }

  @override
  Future<String> addOwn(String text) async {
    final r = await _call('POST', '/v1/own-nodes', {'text': text});
    final added = r['added'] ?? 0, dup = r['duplicates'] ?? 0, failed = r['failed'] ?? 0;
    return 'Добавлено: $added${dup != 0 ? ', уже были: $dup' : ''}${failed != 0 ? ', не разобрано: $failed' : ''}';
  }

  @override
  Future<void> removeOwn(String id) async => _call('DELETE', '/v1/own-nodes/$id');
  @override
  Future<String> nodeLink(String id) async => ((await _call('GET', '/v1/nodes/$id/link'))['link'] as String?) ?? '';

  /// Сообщает ядру, виден ли интерфейс (Android): при скрытом интерфейсе и выключенном VPN фоновые проходы ядра спят. Сбой не важен.
  Future<void> setUiVisible(bool visible) async {
    try {
      await _call('POST', '/v1/ui', {'visible': visible});
    } catch (_) {}
  }

  @override
  Future<MeasureDto> startMeasure(List<String> ids, {bool ping = false, bool exit = false, bool speed = false}) async =>
      MeasureDto.fromJson(await _call('POST', '/v1/measure', {'ids': ids, 'ping': ping, 'exit': exit, 'speed': speed}));

  @override
  Future<TrafficStats> traffic({int n = 120}) async => TrafficStats.fromJson(await _call('GET', '/v1/traffic?n=$n'));

  @override
  Future<List<ConnDto>> connections({bool closed = false, int limit = 300}) async {
    final r = await _callAny('GET', '/v1/connections?limit=$limit${closed ? '&closed=1' : ''}');
    return [for (final e in (r as List)) ConnDto.fromJson(e as Map<String, dynamic>)];
  }

  @override
  Future<void> closeConnection(String id) async => _call('DELETE', '/v1/connections/${Uri.encodeComponent(id)}');

  @override
  Future<List<String>> engineLog({int n = 300}) async {
    final r = await _call('GET', '/v1/logs?n=$n');
    return [for (final e in (r['lines'] as List? ?? const [])) '$e'];
  }

  @override
  Future<List<EventDto>> events({int limit = 200}) async {
    final r = await _callAny('GET', '/v1/events?limit=$limit');
    return [for (final e in (r as List)) EventDto.fromJson(e as Map<String, dynamic>)];
  }

  @override
  Future<NodeDto> connectBest() async => NodeDto.fromJson(await _call('POST', '/v1/connect-best', null, const Duration(seconds: 60)));

  @override
  Future<List<DayTrafficDto>> dailyTraffic({int days = 30}) async {
    final r = await _callAny('GET', '/v1/traffic/daily?days=$days');
    return [
      for (final e in (r as List)) DayTrafficDto((e['day'] as String?) ?? '', (e['up'] as num?)?.toInt() ?? 0, (e['down'] as num?)?.toInt() ?? 0, (e['vpn_up'] as num?)?.toInt() ?? 0, (e['vpn_down'] as num?)?.toInt() ?? 0),
    ];
  }

  @override
  Future<DiagDto> diagnostics() async => DiagDto.fromJson(await _call('POST', '/v1/diagnostics', null, const Duration(seconds: 40)));

  @override
  Future<Map<String, dynamic>> backupExport() => _call('GET', '/v1/backup');

  @override
  Future<String> backupImport(Map<String, dynamic> doc) async {
    final r = await _call('POST', '/v1/backup', doc, const Duration(seconds: 90));
    final skipped = (r['skipped'] as List?)?.length ?? 0;
    return 'Восстановлено: настроек ${r['settings'] ?? 0}, правил ${r['rules'] ?? 0}, серверов ${r['own'] ?? 0}${skipped > 0 ? ', пропущено $skipped' : ''}';
  }

  @override
  Future<String> editOwn(String id, {String? label, String? server, int? port, String? link}) async {
    final r = await _call('PATCH', '/v1/own-nodes/$id', {'label': ?label, 'server': ?server, 'port': ?port, 'link': ?link});
    return (r['id'] as String?) ?? id;
  }

  @override
  Future<void> setLabel(String id, String label) async => _call('PUT', '/v1/nodes/$id/label', {'label': label});

  @override
  Future<MeasureDto> measureState() async => MeasureDto.fromJson(await _call('GET', '/v1/measure'));

  @override
  Future<MeasureDto> cancelMeasure() async => MeasureDto.fromJson(await _call('DELETE', '/v1/measure'));

  @override
  Future<List<UserSetDto>> userSets() async {
    final r = await _call('GET', '/v1/route-sets');
    return [for (final e in (r['sets'] as List? ?? const [])) UserSetDto.fromJson(e as Map<String, dynamic>)];
  }

  @override
  Future<UserSetDto> addUserSet(String url, String title) async =>
      UserSetDto.fromJson(await _call('POST', '/v1/route-sets', {'url': url, 'title': title}, const Duration(minutes: 3)));

  @override
  Future<void> removeUserSet(String id) async => _call('DELETE', '/v1/route-sets/${Uri.encodeComponent(id)}');

  @override
  Future<RouteRulesDto> routeRules() async {
    final r = await _call('GET', '/v1/route-rules');
    return RouteRulesDto(
      [for (final e in (r['rules'] as List? ?? const [])) RouteRuleDto.fromJson(e as Map<String, dynamic>)],
      [for (final e in (r['sets'] as List? ?? const [])) ((e as Map)['name'] as String)],
      countries: [
        for (final e in (r['countries'] as List? ?? const []))
          CountryDto((e as Map)['code'] as String, (e['alive'] as num?)?.toInt() ?? 0, (e['total'] as num?)?.toInt() ?? 0)
      ],
      via: {
        for (final e in (r['via'] as List? ?? const []))
          (e as Map)['rule'] as String: ViaStateDto(
              rule: e['rule'] as String,
              target: (e['target'] as String?) ?? '',
              node: (e['node'] as String?) ?? '',
              fallback: e['fallback'] == true,
              candidates: (e['candidates'] as num?)?.toInt() ?? 0)
      },
    );
  }

  @override
  Future<List<RouteProfileDto>> routeProfiles() async {
    final r = await _call('GET', '/v1/route-profiles');
    return [for (final e in (r['profiles'] as List? ?? const [])) RouteProfileDto.fromJson(e as Map<String, dynamic>)];
  }

  @override
  Future<RouteProfileDto> createRouteProfile(String name, {String from = ''}) async =>
      RouteProfileDto.fromJson(await _call('POST', '/v1/route-profiles', {'name': name, if (from.isNotEmpty) 'from': from}));

  @override
  Future<RouteProfileDto> importRouteProfile(String text, {String name = ''}) async =>
      RouteProfileDto.fromJson(await _call('POST', '/v1/route-profiles/import', {'text': text, if (name.isNotEmpty) 'name': name}));

  @override
  Future<void> renameRouteProfile(String id, String name) async => _call('PUT', '/v1/route-profiles/${Uri.encodeComponent(id)}', {'name': name});

  @override
  Future<void> deleteRouteProfile(String id) async => _call('DELETE', '/v1/route-profiles/${Uri.encodeComponent(id)}');

  @override
  Future<String?> activateRouteProfile(String id) async {
    final r = await _call('POST', '/v1/route-profiles/${Uri.encodeComponent(id)}/activate', null, const Duration(seconds: 90));
    return r['reload_error'] as String?;
  }

  @override
  Future<String> shareRouteProfile(String id) async => (await _call('GET', '/v1/route-profiles/${Uri.encodeComponent(id)}/share'))['link'] as String? ?? '';

  @override
  Future<String?> putRouteRules(List<RouteRuleDto> rules) async {
    final r = await _call('PUT', '/v1/route-rules', {'rules': [for (final e in rules) e.toJson()]}, const Duration(seconds: 90));
    return r['reload_error'] as String?;
  }

  @override
  Future<List<SourceDto>> sources() async {
    final r = await _callAny('GET', '/v1/sources');
    return [for (final e in (r as List)) SourceDto.fromJson(e as Map<String, dynamic>)];
  }

  @override
  Future<void> addSource(String name, String url) async => _call('POST', '/v1/sources', {'name': name, 'url': url});

  @override
  Future<void> setSourceEnabled(String name, bool on) async => _call('POST', '/v1/sources/${Uri.encodeComponent(name)}/enable', {'on': on});

  @override
  Future<void> removeSource(String name) async => _call('DELETE', '/v1/sources/${Uri.encodeComponent(name)}');

  @override
  Future<List<CandidateDto>> sourceCandidates() async {
    final r = await _callAny('GET', '/v1/source-candidates');
    return [for (final e in (r as List)) CandidateDto.fromJson(e as Map<String, dynamic>)];
  }

  @override
  Future<List<CandidateGroupDto>> candidateGroups() async {
    final r = await _callAny('GET', '/v1/source-candidate-groups');
    return [for (final e in (r as List)) CandidateGroupDto.fromJson((e as Map).cast<String, dynamic>())];
  }

  @override
  Future<void> dismissCandidateGroup(String id) async => _call('POST', '/v1/source-candidate-groups/${Uri.encodeComponent(id)}/dismiss');

  @override
  Future<String> addCandidate(String id) async {
    final r = await _call('POST', '/v1/source-candidates/${Uri.encodeComponent(id)}/add');
    return '${r['name'] ?? ''}';
  }

  @override
  Future<void> dismissCandidate(String id) async => _call('POST', '/v1/source-candidates/${Uri.encodeComponent(id)}/dismiss');

  @override
  Future<String> scanCandidates({bool allowMetered = false}) async {
    final r = await _call('POST', '/v1/source-candidates/scan', {'allow_metered': allowMetered}, const Duration(minutes: 10));
    return 'Просмотрено репозиториев: ${r['repos'] ?? 0}, найдено: ${r['found'] ?? 0} (новых ${r['new'] ?? 0})';
  }

  @override
  Future<Map<String, String>> settings() async {
    final r = await _call('GET', '/v1/settings');
    return {for (final e in r.entries) e.key: '${e.value}'};
  }

  @override
  Future<void> putSetting(String key, String value) async => _call('PUT', '/v1/settings/${Uri.encodeComponent(key)}', {'value': value});

  @override
  Future<String> updateRuleSets() async {
    final r = await _call('POST', '/v1/rulesets/update', null, const Duration(minutes: 3));
    final upd = (r['updated'] as List?)?.length ?? 0;
    final failed = (r['failed'] as Map?)?.length ?? 0;
    return failed > 0 ? 'Обновлено $upd, не удалось $failed' : (upd > 0 ? 'Обновлено списков: $upd' : 'Списки актуальны');
  }

  @override
  Future<String> setTunApps(String mode, List<String> apps) async {
    final r = await _call('PUT', '/v1/tun/apps', {'mode': mode, 'apps': apps}, const Duration(seconds: 90));
    return '${r['restart_error'] ?? ''}';
  }

  @override
  Future<UpdateDto> update() async => UpdateDto.fromJson(await _call('GET', '/v1/update'));

  @override
  Future<UpdateDto> checkUpdate() async => UpdateDto.fromJson(await _call('POST', '/v1/update/check', null, const Duration(seconds: 120)));

  @override
  Future<UpdateDto> downloadUpdate({bool allowMetered = false}) async => UpdateDto.fromJson(await _call('POST', '/v1/update/download', {'allow_metered': allowMetered}));

  @override
  Future<UpdateDto> installUpdate({int appPid = 0}) async => UpdateDto.fromJson(await _call('POST', '/v1/update/install', {'app_pid': appPid}, const Duration(seconds: 120)));

  @override
  Future<String> otaSources() async {
    final r = await _call('POST', '/v1/ota/sources', null, const Duration(seconds: 120));
    return '${r['message'] ?? 'готово'}';
  }

  @override
  Future<WarpStateDto> warpState() async => WarpStateDto.fromJson(await _call('GET', '/v1/warp'));

  @override
  Future<List<WarpScanResultDto>> warpScan({bool apply = true, int stopAtGood = 0}) async {
    final r = await _call('POST', '/v1/warp/scan', {'apply': apply, 'stop_at_good': stopAtGood}, const Duration(minutes: 6));
    return [for (final x in (r['results'] as List? ?? const [])) WarpScanResultDto.fromJson(x as Map<String, dynamic>)];
  }

  @override
  Future<void> warpParams(Map<String, String> params) async => _call('PUT', '/v1/warp/params', params, const Duration(seconds: 60));

  @override
  Future<void> warpReset() async => _call('POST', '/v1/warp/reset');

  @override
  Future<String> warpRegister({bool replace = false}) async {
    await _call('POST', '/v1/warp/register', {'replace': replace}, const Duration(seconds: 120));
    return 'WARP настроен';
  }

  @override
  Future<String> warpImport(Map<String, dynamic> account, {bool replace = false}) async {
    await _call('POST', '/v1/warp/import', {'account': account, 'replace': replace}, const Duration(seconds: 60));
    return 'Аккаунт WARP загружен';
  }

  @override
  Future<String> refreshSources() async {
    final r = await _call('POST', '/v1/refresh', null, const Duration(minutes: 3));
    return 'Новых узлов: ${r['added'] ?? 0}, всего ${r['total'] ?? 0}';
  }

  @override
  Future<String> checkNow() async {
    final r = await _call('POST', '/v1/check', null, const Duration(minutes: 3));
    final fails = r['fails'];
    var why = '';
    if (fails is Map && fails.isNotEmpty) {
      final e = fails.entries.toList()..sort((a, b) => (b.value as num).compareTo(a.value as num));
      why = '; отказы: ${e.take(3).map((x) => '${x.key.isEmpty ? 'другое' : x.key} ${x.value}').join(', ')}';
    }
    return 'Проверено ${r['checked'] ?? 0}, живых ${r['ok'] ?? 0}$why';
  }

  /// Долгий опрос уведомлений демона (Windows: служба показывать их не может, показывает приложение). Ждёт до 25 с.
  Future<NoticeBatch> pollNotices(int after) async =>
      NoticeBatch.fromJson(await _call('GET', '/v1/notifications?after=$after&wait=25', null, const Duration(seconds: 35)));

  /// Нажатие кнопки уведомления.
  Future<void> noticeAction(int id, String key) async => _call('POST', '/v1/notifications/$id/action', {'key': key});

  @override
  void close() {
    _client.close(force: true);
    _plain.close(force: true);
  }
}


/// Прокси для Telegram.
class TgProxyDto {
  const TgProxyDto({
    required this.id,
    required this.kind,
    this.mode = '',
    required this.server,
    required this.port,
    required this.link,
    this.favorite = false,
    this.own = false,
    this.ok = false,
    this.latencyMs = 0,
    this.checked = false,
    this.checkedAt,
  });

  final String id;
  final String kind; // mtproto | socks5 | webproxy
  final String mode; // plain | secure | faketls ('' у SOCKS5)
  final String server;
  final int port;
  final String link; // tg://… для открытия в Telegram
  final bool favorite;
  final bool own;
  final bool ok; // последняя проверка успешна
  final int latencyMs;
  final bool checked;
  final DateTime? checkedAt;

  factory TgProxyDto.fromJson(Map<String, dynamic> j) => TgProxyDto(
        id: j['id'] as String,
        kind: (j['kind'] as String?) ?? 'mtproto',
        mode: (j['mode'] as String?) ?? '',
        server: (j['server'] as String?) ?? '',
        port: (j['port'] as num?)?.toInt() ?? 0,
        link: (j['link'] as String?) ?? '',
        favorite: j['favorite'] == true,
        own: j['own'] == true,
        ok: j['ok'] == true,
        latencyMs: (j['latency_ms'] as num?)?.toInt() ?? 0,
        checked: j['checked'] == true,
        checkedAt: j['checked_at'] is String ? DateTime.tryParse(j['checked_at'] as String) : null,
      );

  /// Подпись вида: что за прокси и как маскируется.
  String get kindLabel => kind == 'webproxy' ? 'WEB-proxy' : switch (mode) {
        'faketls' => 'MTProto · FakeTLS',
        'secure' => 'MTProto · Secure',
        'plain' => 'MTProto',
        _ => kind == 'socks5' ? 'SOCKS5' : 'MTProto',
      };

  /// Сигнатура для сравнения списков (перерисовка только при изменениях).
  String get sig => '$id|$ok|$latencyMs|$favorite|${checkedAt?.millisecondsSinceEpoch ?? 0}';
}

class TgProxyList {
  const TgProxyList({this.items = const [], this.total = 0, this.alive = 0});

  final List<TgProxyDto> items;
  final int total;
  final int alive;

  factory TgProxyList.fromJson(Map<String, dynamic> j) => TgProxyList(
        items: [for (final e in (j['items'] as List? ?? const [])) TgProxyDto.fromJson(e as Map<String, dynamic>)],
        total: (j['total'] as num?)?.toInt() ?? 0,
        alive: (j['alive'] as num?)?.toInt() ?? 0,
      );
}
