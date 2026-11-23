import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/apps_page.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/country.dart';
import 'package:brod/src/daemon.dart';
import 'package:brod/src/extras.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/tray.dart';
import 'package:brod/src/ui.dart';

class FakeApi implements DaemonApi {
  VpnState state = const VpnState(on: false, mode: 'blocked');
  Object? failSet;
  List<String> calls = [];
  int probeOkAfter = 0; // сколько проверок вернут «не работает»
  int _probes = 0;

  @override
  Future<VpnState> vpn() async => state;

  @override
  Future<VpnState> setVpn({bool? on, String? mode, String? autoconnect}) async {
    calls.add(autoconnect != null ? 'autoconnect $autoconnect' : 'set on=$on mode=$mode');
    if (failSet != null) throw failSet!;
    state = VpnState(
        on: on ?? state.on, mode: mode ?? state.mode, iface: (on ?? state.on) ? 'vpnclient0' : '', autoconnect: autoconnect ?? state.autoconnect);
    return state;
  }

  Activity activity = const Activity(); // что «делает» демон (индикация загрузки)

  @override
  Future<DaemonStatus> status() async => DaemonStatus(
      version: '0.3-dev', nodes: 12, present: 10, degraded: false, warpEnabled: true, activity: activity, active: const ActiveNode(proto: 'warp', server: 'Cloudflare WARP', port: 0));

  List<bool> probeQuick = []; // с каким флагом приходили проверки
  @override
  Future<ProbeResult> probe({bool quick = false}) async {
    probeQuick.add(quick);
    return ++_probes > probeOkAfter ? const ProbeResult(ok: true, latencyMs: 350) : const ProbeResult(ok: false, detail: 'timeout');
  }

  List<String> ownTexts = [];

  @override
  Future<String> addOwn(String text) async {
    ownTexts.add(text);
    return 'Добавлено: 1';
  }

  @override
  Future<void> removeOwn(String id) async => calls.add('rmOwn $id');
  @override
  Future<String> nodeLink(String id) async => 'vless://fake-$id';

  @override
  Future<List<NodeDto>> nodes({bool alive = false, bool favorites = false, int limit = 300, String source = ''}) async => (++nodesCalls) < 0 ? [] : source == 'own' ? [const NodeDto(id: 'o1', proto: 'vless', server: 'vps.example', port: 443, own: true)] : [
        NodeDto(id: 'a1', proto: 'vless', server: 'one.example', port: 443, youtube: true, l2At: DateTime.now(), exit: const ExitDto(ip: '1.1.1.1', country: 'NL', clean: 'clean'), latencyMs: 300, speedKBps: 4096),
        NodeDto(id: 'b2', proto: 'trojan', server: 'two.example', port: 8443, favorite: true, l2At: DateTime.now().subtract(const Duration(minutes: 5)), exit: const ExitDto(ip: '2.2.2.2', country: 'DE', clean: 'dirty'), latencyMs: 100, speedKBps: 512),
        const NodeDto(id: 'c3', proto: 'ss', server: 'three.example', port: 1),
      ];

  @override
  Future<void> connectNode(String id) async => calls.add('connect $id');

  @override
  Future<void> setFavorite(String id, bool on) async => calls.add('fav $id $on');

  // прокси Telegram
  List<TgProxyDto> tgList = [
    const TgProxyDto(id: 'p1', kind: 'mtproto', mode: 'faketls', server: 'fast.example', port: 443, link: 'tg://proxy?server=fast.example&port=443&secret=ee00', ok: true, latencyMs: 250, checked: true),
    const TgProxyDto(id: 'p2', kind: 'mtproto', mode: 'plain', server: 'slow.example', port: 8443, link: 'tg://proxy?server=slow.example&port=8443&secret=00', ok: true, latencyMs: 900, checked: true, favorite: true),
    const TgProxyDto(id: 'p3', kind: 'socks5', server: 'dead.example', port: 1080, link: 'tg://socks?server=dead.example&port=1080', ok: false, checked: true, own: true),
  ];
  List<String> tgCalls = [];
  Completer<void>? tgGate;
  @override
  Future<TgProxyList> tgProxies({bool alive = false, bool fav = false, String kind = '', int limit = 300}) async {
    tgCalls.add('list alive=$alive fav=$fav kind=$kind');
    final l = [for (final p in tgList) if ((!alive || p.ok) && (!fav || p.favorite) && (kind.isEmpty || p.kind == kind)) p];
    return TgProxyList(items: l, total: tgList.length, alive: tgList.where((p) => p.ok).length);
  }

  @override
  Future<int> tgAdd(String text) async {
    tgCalls.add('add');
    return text.contains('tg://') ? 1 : throw ApiException(400, 'ссылок Telegram-прокси не найдено');
  }

  @override
  Future<String> tgCheck() async {
    tgCalls.add('check');
    if (tgGate != null) await tgGate!.future;
    return 'Проверено 3, живых 2';
  }

  @override
  Future<void> tgDelete(String id) async => tgCalls.add('delete $id');

  @override
  Future<void> tgFavorite(String id, bool on) async => tgCalls.add('fav $id $on');

  @override
  Future<double> measureSpeed(String id) async {
    calls.add('speed $id');
    return 1024;
  }

  @override
  Future<({String grade, int medianMs, int lossPct})> probeUdp(String id) async {
    calls.add('udp $id');
    return (grade: 'good', medianMs: 70, lossPct: 0);
  }

  @override
  Future<TrafficStats> traffic({int n = 120}) async =>
      TrafficStats(samples: [for (var i = 0; i < 5; i++) TrafficSample(1000 + i, 100 * i, 1000 * i)], upNow: 400, downNow: 4000, upTotal: 5000, downTotal: 50000, conns: 2);

  @override
  Future<List<ConnDto>> connections({bool closed = false, int limit = 300}) async => [
        ConnDto(id: 'c1', network: 'tcp', dest: 'example.com:443', route: 'proxy', node: 'WARP', up: 10, down: 20, started: DateTime.now()),
        if (closed) ConnDto(id: 'c2', network: 'udp', dest: 'dns:53', route: 'direct', node: 'напрямую', started: DateTime.now(), closed: DateTime.now()),
      ];

  @override
  Future<void> closeConnection(String id) async => calls.add('close $id');

  @override
  Future<List<String>> engineLog({int n = 300}) async => ['2026-01-01 INFO started', '2026-01-01 ERROR boom'];

  @override
  Future<List<EventDto>> events({int limit = 200}) async => [EventDto(id: 1, at: DateTime.now(), kind: 'switch', initiator: 'user', detail: 'x')];

  @override
  Future<NodeDto> connectBest() async {
    calls.add('best');
    return const NodeDto(id: 'a1', proto: 'vless', server: 'one.example', port: 443);
  }

  @override
  Future<List<DayTrafficDto>> dailyTraffic({int days = 30}) async => const [DayTrafficDto('2026-11-03', 10, 100, 5, 50)];

  @override
  Future<DiagDto> diagnostics() async => const DiagDto(ip: '1.2.3.4', country: 'NL', findings: [DiagFindingDto('ok', 'Сайты видят адрес выхода', '1.2.3.4')]);

  Map<String, dynamic>? lastBackup;

  @override
  Future<Map<String, dynamic>> backupExport() async => {'version': 1, 'settings': {'degrade.action': 'auto'}};

  @override
  Future<String> backupImport(Map<String, dynamic> doc) async {
    lastBackup = doc;
    return 'Восстановлено: настроек 1';
  }

  @override
  Future<String> editOwn(String id, {String? label, String? server, int? port, String? link}) async {
    calls.add('edit $id label=$label server=$server port=$port link=$link');
    return id;
  }

  @override
  Future<void> setLabel(String id, String label) async => calls.add('label $id $label');

  MeasureDto ms = const MeasureDto();

  @override
  Future<MeasureDto> startMeasure(List<String> ids, {bool ping = false, bool exit = false, bool speed = false}) async {
    calls.add('measure ${ids.join(",")} p=$ping e=$exit s=$speed');
    ms = MeasureDto(running: true, total: ids.length, done: 0, stage: 'speed');
    return ms;
  }

  @override
  Future<MeasureDto> measureState() async {
    ms = MeasureDto(running: false, total: ms.total, done: ms.total, ok: ms.total);
    return ms;
  }

  @override
  Future<MeasureDto> cancelMeasure() async => ms;

  @override
  Future<ExitDto> exitInfo(String id) async => const ExitDto(ip: '203.0.113.1', country: 'DE', clean: 'clean');

  List<RouteRuleDto> savedRules = [];

  @override
  Future<RouteRulesDto> routeRules() async => RouteRulesDto(savedRules.map((e) => e.copy()).toList(), const ['geosite-youtube']);

  UpdateDto updateState = const UpdateDto(current: '0.3.0', platform: 'linux-amd64');
  bool updateAfterCheck = true; // проверка находит версию 0.4.0

  @override
  Future<UpdateDto> update() async => updateState;

  @override
  Future<UpdateDto> checkUpdate() async {
    calls.add('update check');
    updateState = updateAfterCheck
        ? UpdateDto(current: '0.3.0', platform: 'linux-amd64', checked: DateTime(2026, 11, 10), available: true, app: '0.4.0', notes: 'правила маршрутизации', size: 91 << 20, sha256: 'ab' * 32)
        : UpdateDto(current: '0.3.0', platform: 'linux-amd64', checked: DateTime(2026, 11, 10), error: 'нет сети');
    return updateState;
  }

  List<UserSetDto> userSetsState = [];

  @override
  Future<List<UserSetDto>> userSets() async => userSetsState;

  @override
  Future<UserSetDto> addUserSet(String url, String title) async {
    calls.add('add set $url');
    final s = UserSetDto(id: 'ext-0a1b2c3d', title: title, url: url, size: 1234);
    userSetsState = [...userSetsState, s];
    return s;
  }

  @override
  Future<void> removeUserSet(String id) async {
    calls.add('remove set $id');
    userSetsState = [for (final s in userSetsState) if (s.id != id) s];
  }

  @override
  Future<UpdateDto> installUpdate({int appPid = 0}) async {
    calls.add('update install');
    return updateState;
  }

  @override
  Future<UpdateDto> downloadUpdate({bool allowMetered = false}) async {
    calls.add(allowMetered ? 'update download metered' : 'update download');
    final u = updateState;
    updateState = UpdateDto(
        current: u.current, platform: u.platform, checked: u.checked, available: true, app: u.app, notes: u.notes, size: u.size, sha256: u.sha256,
        download: UpdateDownloadDto(state: 'ready', bytes: u.size, total: u.size, path: '/tmp/brod-0.4.0-linux-amd64.tar.gz'));
    return updateState;
  }

  List<RouteProfileDto> profiles = const [RouteProfileDto(id: 'main', name: 'Основной', active: true)];

  @override
  Future<List<RouteProfileDto>> routeProfiles() async => profiles;

  @override
  Future<RouteProfileDto> createRouteProfile(String name, {String from = ''}) async {
    calls.add('profile create $name from=$from');
    final p = RouteProfileDto(id: 'p${profiles.length}', name: name);
    profiles = [...profiles, p];
    return p;
  }

  @override
  Future<RouteProfileDto> importRouteProfile(String text, {String name = ''}) async {
    calls.add('profile import $name');
    final p = RouteProfileDto(id: 'p${profiles.length}', name: name.isEmpty ? 'Импортированный' : name, rules: 1);
    profiles = [...profiles, p];
    return p;
  }

  @override
  Future<void> renameRouteProfile(String id, String name) async => calls.add('profile rename $id $name');

  @override
  Future<void> deleteRouteProfile(String id) async {
    calls.add('profile delete $id');
    profiles = profiles.where((p) => p.id != id).toList();
  }

  @override
  Future<String?> activateRouteProfile(String id) async {
    calls.add('profile activate $id');
    profiles = [for (final p in profiles) RouteProfileDto(id: p.id, name: p.name, rules: p.rules, active: p.id == id)];
    return null;
  }

  @override
  Future<String> shareRouteProfile(String id) async => 'brod://route/test-$id';

  @override
  Future<String?> putRouteRules(List<RouteRuleDto> rules) async {
    calls.add('putRules ${rules.length}');
    savedRules = rules.map((e) => e.copy()).toList();
    return null;
  }

  List<SourceDto> srcs = [const SourceDto(name: 'main', host: 'example.org', enabled: true, userAdded: false, lastCount: 10)];

  @override
  Future<List<SourceDto>> sources() async => srcs;

  @override
  Future<void> addSource(String name, String url) async {
    calls.add('addSource $name');
    srcs = [...srcs, SourceDto(name: name, host: 'x', enabled: true, userAdded: true)];
  }

  @override
  Future<void> setSourceEnabled(String name, bool on) async => calls.add('src $name $on');

  List<CandidateDto> cands = [];

  @override
  Future<List<CandidateDto>> sourceCandidates() async => cands;

  @override
  Future<List<CandidateGroupDto>> candidateGroups() async => [for (final c in cands) CandidateGroupDto(id: 'g${c.id}', representative: c)];

  @override
  Future<void> dismissCandidateGroup(String id) async => calls.add('dismissCandidateGroup $id');

  @override
  Future<String> addCandidate(String id) async {
    calls.add('addCandidate $id');
    return id;
  }

  @override
  Future<void> dismissCandidate(String id) async => calls.add('dismissCandidate $id');

  @override
  Future<String> scanCandidates({bool allowMetered = false}) async {
    calls.add('scanCandidates');
    return 'ok';
  }

  @override
  Future<void> removeSource(String name) async => calls.add('rmSource $name');

  Map<String, String> sett = {'check.via_warp': '0', 'degrade.action': 'notify', 'notify.level': 'important'};

  @override
  Future<Map<String, String>> settings() async => sett;

  @override
  Future<void> putSetting(String key, String value) async {
    calls.add('set $key=$value');
    sett = {...sett, key: value};
  }

  @override
  Future<String> updateRuleSets() async => 'Списки актуальны';

  String tunAppsRestartError = '';
  @override
  Future<String> setTunApps(String mode, List<String> apps) async {
    calls.add('tun apps $mode ${apps.join(',')}');
    sett = {...sett, 'tun.apps_mode': mode, 'tun.apps': apps.join(',')};
    return tunAppsRestartError;
  }

  @override
  Future<String> otaSources() async {
    calls.add('ota sources');
    return 'версия 3: новых 2, изменено 0, снято 0';
  }

  @override
  Future<WarpStateDto> warpState() async => const WarpStateDto();

  @override
  Future<ProxyShareDto> proxyShare() async => const ProxyShareDto(lan: true, addrs: ['192.168.1.5'], port: 29080, username: 'u', password: 'p');

  final chains = <(String, List<String>)>[];

  @override
  Future<List<HopCheckDto>> checkChain(String id) async => const [];

  @override
  Future<String> addChain(String name, List<String> hops) async {
    chains.add((name, hops));
    return 'chain-id';
  }

  ReportStateDto reportNext = const ReportStateDto(state: 'idle');
  int reportStarts = 0;

  @override
  Future<void> reportStart() async => reportStarts++;

  @override
  Future<ReportStateDto> reportState() async => reportNext;

  @override
  Future<List<WarpScanResultDto>> warpScan({bool apply = true, int stopAtGood = 0}) async => const [];

  @override
  Future<void> warpParams(Map<String, String> params) async {}

  @override
  Future<void> warpReset() async {}

  @override
  Future<String> warpRegister({bool replace = false}) async {
    calls.add('warp register replace=$replace');
    return 'WARP настроен';
  }

  @override
  Future<String> warpImport(Map<String, dynamic> account, {bool replace = false}) async {
    calls.add('warp import ${account.keys.length} replace=$replace');
    return 'Аккаунт WARP загружен';
  }

  @override
  Future<String> refreshSources() async => 'ok';

  Completer<void>? checkGate; // если задан, проверка узлов «идёт», пока его не завершат
  int nodesCalls = 0;
  @override
  Future<String> checkNow() async {
    if (checkGate != null) await checkGate!.future;
    return 'ok';
  }

  @override
  void close() {}
}

class AliveRunner extends DaemonRunner {
  AliveRunner(super.api);
  @override
  Future<String?> ensureRunning() async => null;
}

class DownApi extends FakeApi {
  @override
  Future<VpnState> vpn() async => throw ApiException(0, 'сокет не отвечает');
}

class HeldRunner extends AliveRunner {
  HeldRunner(super.api);
  int released = 0;
  @override
  bool killswitchMarkerExists() => true;
  @override
  Future<String?> releaseKillswitch() async {
    released++;
    return null;
  }
}

void main() {
  test('состояние VPN несёт киллсвитч и не теряет его при смене режима', () {
    final v = VpnState.fromJson({'on': true, 'mode': 'all', 'interface': 'vpnclient0', 'killswitch': 'failed', 'killswitch_error': 'нет права'});
    expect(v.killswitch, 'failed');
    expect(v.killswitchError, 'нет права');
    final w = v.withMode('all-but-ru');
    expect(w.mode, 'all-but-ru');
    expect(w.killswitch, 'failed');
    expect(w == v, false);
    expect(VpnState.fromJson({'on': false, 'mode': 'blocked'}).killswitch, '');
    final c = VpnState.fromJson({'on': false, 'mode': 'all', 'conflict': ['throne-tun']});
    expect(c.conflict, ['throne-tun']);
    expect(c.withMode('blocked').conflict, ['throne-tun']);
    expect(c == VpnState.fromJson({'on': false, 'mode': 'all'}), false);
  });

  test('автоподключение: читается из состояния, переживает смену режима и меняется через демон', () async {
    final v = VpnState.fromJson({'on': true, 'mode': 'all', 'autoconnect': 'always'});
    expect(v.autoconnect, 'always');
    expect(v.withMode('blocked').autoconnect, 'always');
    expect(VpnState.fromJson({'on': false, 'mode': 'all'}).autoconnect, '', reason: 'Android: поля нет — пункт в настройках не показывается');
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.setAutoconnect('off');
    expect(api.calls, contains('autoconnect off'));
    expect(m.vpn?.autoconnect, 'off');
    m.dispose();
  });

  test('статус: режим мобильной сети читается из ответа, по умолчанию не определён', () {
    expect(DaemonStatus.fromJson({'version': 'x', 'nodes': 1, 'present': 1, 'net_mode': 'whitelist'}).netMode, 'whitelist');
    expect(DaemonStatus.fromJson({'version': 'x', 'nodes': 1, 'present': 1}).netMode, '');
  });

  test('главный опрос на экране не чаще раза в 5 секунд', () {
    expect(AppModel(FakeApi()).pollEvery, const Duration(seconds: 5));
  });

  testWidgets('в фоне главный опрос замедляется, при возврате на экран возвращается прежний и идёт сразу', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(seconds: 2));
    await m.start();
    expect(m.foreground, isTrue);
    expect(m.currentPoll, const Duration(seconds: 2));
    m.setForeground(false);
    expect(m.foreground, isFalse);
    expect(m.currentPoll, m.backgroundPoll);
    final before = api.calls.length;
    await tester.pump(const Duration(seconds: 10));
    expect(api.calls.length, before, reason: 'за 10 с в фоне (интервал 15 с) опроса быть не должно');
    m.setForeground(true);
    expect(m.currentPoll, const Duration(seconds: 2));
    m.dispose();
  });

  testWidgets('уведомление о результате показывается один раз и не повторяется на другой странице', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    await m.connectBest();
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.textContaining('Выбран лучший узел'), findsOneWidget);
    expect(m.toast, isNull, reason: 'сообщение забрано показывателем и очищено');
    // уведомление исчезает само
    await tester.pump(const Duration(seconds: 4));
    await tester.pump(const Duration(milliseconds: 500));
    expect(find.textContaining('Выбран лучший узел'), findsNothing);
    // переход на «Узлы» и «Настройки» не возвращает его
    await tester.tap(find.text('Узлы'));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.tap(find.text('Настройки'));
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.textContaining('Выбран лучший узел'), findsNothing, reason: 'раньше при заходе в настройки оно показывалось снова');
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('раздельное туннелирование: сохранённое приложение отмечено галочкой при повторном открытии', (tester) async {
    final api = FakeApi();
    api.sett = {...api.sett, 'tun.apps_mode': 'exclude', 'tun.apps': 'ua.itaysonlab.vkx'};
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadSettings();
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(const MethodChannel('dev.godbtw.brod/core'), (call) async {
      if (call.method == 'listApps') {
        return [
          {'package': 'org.telegram.messenger', 'label': 'Telegram'},
          {'package': 'ua.itaysonlab.vkx', 'label': 'VKX'},
        ];
      }
      return null;
    });
    addTearDown(() => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(const MethodChannel('dev.godbtw.brod/core'), null));
    await tester.pumpWidget(MaterialApp(home: AppsPage(model: m)));
    await tester.pumpAndSettle();
    final vkx = tester.widget<CheckboxListTile>(find.widgetWithText(CheckboxListTile, 'VKX'));
    expect(vkx.value, true, reason: 'сохранённое приложение должно быть отмечено');
    expect(tester.widget<CheckboxListTile>(find.widgetWithText(CheckboxListTile, 'Telegram')).value, false);
    expect(tester.widget<ChoiceChip>(find.widgetWithText(ChoiceChip, 'Все, кроме выбранных')).selected, true);
    m.dispose();
  });

  testWidgets('раздельное туннелирование: страница открыта раньше загрузки настроек — галочка появляется, когда они пришли', (tester) async {
    final api = FakeApi();
    api.sett = {...api.sett, 'tun.apps_mode': 'exclude', 'tun.apps': 'ua.itaysonlab.vkx'};
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start(); // настройки намеренно НЕ загружены: как при открытии страницы сразу после запуска
    expect(m.settings, isEmpty);
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(const MethodChannel('dev.godbtw.brod/core'), (call) async {
      return call.method == 'listApps'
          ? [
              {'package': 'ua.itaysonlab.vkx', 'label': 'VKX'},
              {'package': 'org.telegram.messenger', 'label': 'Telegram'},
            ]
          : null;
    });
    addTearDown(() => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(const MethodChannel('dev.godbtw.brod/core'), null));
    await tester.pumpWidget(MaterialApp(home: AppsPage(model: m)));
    await tester.pumpAndSettle();
    expect(tester.widget<CheckboxListTile>(find.widgetWithText(CheckboxListTile, 'VKX')).value, true);
    expect(tester.widget<ChoiceChip>(find.widgetWithText(ChoiceChip, 'Все, кроме выбранных')).selected, true);
    // пользователь начал менять: поздние настройки его выбор не затирают
    await tester.tap(find.widgetWithText(CheckboxListTile, 'Telegram'));
    await tester.pump();
    await m.loadSettings();
    await tester.pump();
    expect(tester.widget<CheckboxListTile>(find.widgetWithText(CheckboxListTile, 'Telegram')).value, true);
    m.dispose();
  });

  testWidgets('журнал: строки движка видны, пауза замораживает список', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    await tester.tap(find.text('Монитор'));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.tap(find.text('Журнал'));
    await tester.pumpAndSettle(const Duration(milliseconds: 100));
    expect(find.text('События'), findsOneWidget, reason: 'вкладка журнала открылась');
    await tester.tap(find.text('Движок'));
    await tester.pumpAndSettle(const Duration(milliseconds: 100));
    expect(find.textContaining('boom'), findsOneWidget);
    await tester.tap(find.byIcon(Icons.vertical_align_bottom)); // пауза
    await tester.pump();
    expect(find.byIcon(Icons.pause), findsOneWidget);
    await tester.tap(find.byIcon(Icons.pause)); // снова следим
    await tester.pump();
    expect(find.byIcon(Icons.vertical_align_bottom), findsOneWidget);
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('вкладка «Прокси»: список, фильтры, открытие в Telegram, «Лучший»', (tester) async {
    final api = FakeApi();
    final opened = <String>[];
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1), opener: (url) async {
      opened.add(url);
      return null;
    });
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    await tester.tap(find.text('Прокси'));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 100));
    // по умолчанию — только живые
    expect(find.text('fast.example:443'), findsOneWidget);
    expect(find.text('slow.example:8443'), findsOneWidget);
    expect(find.text('dead.example:1080'), findsNothing);
    expect(find.textContaining('Живых: 2 из 3'), findsOneWidget);
    expect(find.textContaining('MTProto · FakeTLS'), findsOneWidget);
    expect(find.textContaining('250 мс'), findsWidgets);
    // «Лучший» — наименьший пинг
    expect(find.textContaining('Лучший · 250 мс'), findsOneWidget);
    await tester.tap(find.textContaining('Лучший · 250 мс'));
    await tester.pump();
    expect(opened, ['tg://proxy?server=fast.example&port=443&secret=ee00']);
    // нажатие на строку открывает именно её
    await tester.tap(find.text('slow.example:8443'));
    await tester.pump();
    expect(opened.last, 'tg://proxy?server=slow.example&port=8443&secret=00');
    // убрать «Живые» — виден и неответивший, свой помечен
    await tester.tap(find.text('Живые'));
    await tester.pump(const Duration(milliseconds: 200));
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.text('dead.example:1080'), findsOneWidget);
    expect(find.textContaining('не отвечает'), findsOneWidget);
    expect(find.textContaining('SOCKS5'), findsWidgets);
    expect(find.textContaining('· свой'), findsOneWidget);
    // фильтр по виду
    await tester.tap(find.text('SOCKS5').first);
    await tester.pump(const Duration(milliseconds: 200));
    await tester.pump(const Duration(milliseconds: 100));
    expect(api.tgCalls.last, 'list alive=false fav=false kind=socks5');
    expect(find.text('fast.example:443'), findsNothing);
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  test('прокси Telegram: добавление, избранное, удаление, ошибка открытия и проверка с подгрузкой по ходу', () async {
    final api = FakeApi();
    var fail = false;
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1), opener: (url) async => fail ? 'нет Telegram' : null);
    await m.start();
    await Future<void>.delayed(const Duration(milliseconds: 100));
    await m.loadTgProxies();
    expect(m.tgProxies.length, 2, reason: 'по умолчанию только живые');
    expect(m.tgBest!.id, 'p1');

    await m.tgAddText('мусор');
    expect(m.toast, 'ссылок Telegram-прокси не найдено');
    expect(m.toastError, true);
    await m.tgAddText('tg://proxy?server=x&port=1&secret=00');
    expect(m.toast, startsWith('Добавлено: 1'));
    expect(m.tgAliveOnly, false, reason: 'после добавления показываем и непроверенные, иначе новый прокси не виден');

    await m.tgToggleFav(m.tgProxies.first);
    await m.tgRemove(m.tgProxies.last);
    expect(api.tgCalls, containsAll(['fav p1 true', 'delete p3']));

    await m.openTg(m.tgProxies.first);
    expect(m.toast, 'Подтвердите прокси в окне Telegram');
    fail = true;
    await m.openTg(m.tgProxies.first);
    expect(m.toast, 'нет Telegram');
    expect(m.toastError, true);

    // проверка идёт долго: список перечитывается каждые 2 с, флаг загрузки держится до конца
    api.tgGate = Completer<void>();
    final before = api.tgCalls.where((c) => c.startsWith('list')).length;
    final run = m.tgCheckNow();
    await Future<void>.delayed(const Duration(milliseconds: 2400));
    expect(m.tgLoading, true);
    expect(api.tgCalls.where((c) => c.startsWith('list')).length - before, greaterThanOrEqualTo(1));
    api.tgGate!.complete();
    await run;
    expect(m.tgLoading, false);
    expect(m.toast, 'Проверено 3, живых 2');
    m.dispose();
  });

  testWidgets('индикация загрузки узлов: обновление, проверка с прогрессом, пустой список объясняет, что идёт', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    await tester.tap(find.text('Узлы'));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.byKey(const Key('node-activity')), findsNothing, reason: 'ничего не идёт — полосы нет');

    api.activity = const Activity(refreshing: true);
    await m.refresh();
    await tester.pump();
    expect(m.status!.activity.refreshing, true, reason: 'статус обновился');
    expect(find.text('Обновляю подписки…'), findsOneWidget);

    api.activity = const Activity(checking: true, checkPicked: 200, checkDone: 120, checkOk: 37);
    await m.refresh();
    await tester.pump();
    expect(find.text('Проверяю узлы: 120 из 200 · живых 37'), findsOneWidget);
    final bar = tester.widget<LinearProgressIndicator>(find.descendant(of: find.byKey(const Key('node-activity')), matching: find.byType(LinearProgressIndicator)));
    expect(bar.value, closeTo(0.6, 0.001));

    api.activity = const Activity();
    await m.refresh();
    await tester.pump();
    expect(find.byKey(const Key('node-activity')), findsNothing, reason: 'после окончания полоса исчезает');
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('индикация проверки прокси Telegram', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1), opener: (_) async => null);
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    await tester.tap(find.text('Прокси'));
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('tg-activity')), findsNothing);
    api.activity = const Activity(tgChecking: true, tgPicked: 120, tgDone: 30, tgOk: 11);
    await m.refresh();
    await tester.pump();
    expect(find.text('Проверяю прокси: 30 из 120 · живых 11'), findsOneWidget);
    api.activity = const Activity();
    await m.refresh();
    await tester.pump();
    expect(find.byKey(const Key('tg-activity')), findsNothing);
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('главная: чужой туннель — плашка с объяснением', (tester) async {
    final api = FakeApi()..state = const VpnState(on: false, mode: 'all', conflict: ['throne-tun']);
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    expect(find.textContaining('Работает другой VPN (throne-tun)'), findsOneWidget);
    api.state = const VpnState(on: false, mode: 'all');
    await m.refresh();
    await tester.pump();
    expect(find.textContaining('Работает другой VPN'), findsNothing);
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('главная: киллсвитч виден в подписи, сбой установки — в плашке', (tester) async {
    final api = FakeApi()..state = const VpnState(on: true, mode: 'all', iface: 'vpnclient0', killswitch: 'active');
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    expect(find.textContaining('киллсвитч'), findsOneWidget);
    expect(find.textContaining('Киллсвитч не включился'), findsNothing);
    api.state = const VpnState(on: true, mode: 'all', iface: 'vpnclient0', killswitch: 'failed', killswitchError: 'нет права cap_net_admin');
    await m.refresh();
    await tester.pump();
    expect(find.textContaining('Киллсвитч не включился: нет права cap_net_admin'), findsOneWidget);
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('демон упал при стоящем киллсвитче: объяснение и кнопка снятия блокировки', (tester) async {
    final api = DownApi();
    final runner = HeldRunner(api);
    final m = AppModel(api, runner: runner, pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    expect(find.text('Демон остановился, трафик заблокирован'), findsOneWidget);
    expect(find.textContaining('sudo nft delete table inet brod_ks'), findsOneWidget);
    await tester.tap(find.text('Снять блокировку и запустить демон'));
    await tester.pump();
    expect(runner.released, 1);
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  test('«обновить и проверить»: список узлов подгружается по ходу проверки, а не только в конце', () async {
    final api = FakeApi()..checkGate = Completer<void>();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await Future<void>.delayed(const Duration(milliseconds: 100)); // стартовая загрузка списка (start её не ждёт) должна закончиться
    final base = api.nodesCalls;
    final run = m.refreshAndCheck();
    await Future<void>.delayed(const Duration(milliseconds: 2400));
    expect(m.nodesLoading, true, reason: 'проверка ещё идёт');
    expect(api.nodesCalls - base, greaterThanOrEqualTo(2), reason: 'после обновления подписок и раз в 2 с список перечитывается');
    expect(m.nodes, isNotEmpty);
    api.checkGate!.complete();
    await run;
    expect(m.nodesLoading, false);
    final after = api.nodesCalls;
    await Future<void>.delayed(const Duration(milliseconds: 2300));
    expect(api.nodesCalls, after, reason: 'после конца проверки опрос списка остановлен');
    m.dispose();
  });

  test('включение ждёт рабочего выхода, режим применяется', () async {
    final api = FakeApi()..probeOkAfter = 2;
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    expect(m.phase, Phase.ready);
    expect(m.vpn!.on, false);
    await m.setMode('all');
    expect(api.state.mode, 'all');
    expect(api.state.on, false);
    await m.setOn(true);
    expect(m.vpn!.on, true);
    expect(m.probe!.ok, true, reason: 'после включения дождались рабочего выхода');
    expect(m.connecting, false);
    expect(api.calls, ['set on=null mode=all', 'set on=true mode=null']);
    // ожидание выхода идёт лёгкой проверкой; строгая запускается следом, не задерживая «подключено»
    await Future<void>.delayed(const Duration(milliseconds: 50));
    expect(api.probeQuick.first, true, reason: 'ожидание — быстрой проверкой');
    expect(api.probeQuick.where((q) => q).length, 3, reason: 'две неудачи и успех');
    expect(api.probeQuick.last, false, reason: 'после успеха — одна строгая проверка');
    m.dispose();
  });

  test('ошибка включения показывается, демон остаётся', () async {
    final api = FakeApi()..failSet = ApiException(409, 'нет прав на TUN');
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.setOn(true);
    expect(m.notice, 'нет прав на TUN');
    expect(m.phase, Phase.ready);
    expect(m.busy, false);
    m.dispose();
  });

  testWidgets('окно: переключатель и режимы', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    expect(find.text('VPN выключен'), findsOneWidget);
    expect(find.text('Cloudflare WARP'), findsOneWidget);

    await tester.tap(find.text('Всё через выход'));
    await tester.pump();
    expect(api.state.mode, 'all');

    await tester.tap(find.byKey(const Key('power')));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(find.text('VPN включён'), findsOneWidget);
    m.dispose();
  });

  test('список узлов: фильтры и сортировка', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadNodes();
    expect(m.visibleNodes.map((n) => n.id), ['a1', 'b2', 'c3'], reason: 'свежие первыми, непроверенный последним');
    m.setNodeView(youtube: true);
    expect(m.visibleNodes.map((n) => n.id), ['a1']);
    m.setNodeView(youtube: false, fav: true);
    expect(m.visibleNodes.map((n) => n.id), ['b2']);
    m.setNodeView(fav: false, query: 'three');
    expect(m.visibleNodes.map((n) => n.id), ['c3']);
    m.setNodeView(query: '', order: NodeSort.proto);
    expect(m.visibleNodes.map((n) => n.proto), ['ss', 'trojan', 'vless']);
    m.setNodeView(order: NodeSort.country);
    expect(m.visibleNodes.map((n) => n.id), ['b2', 'a1', 'c3'], reason: 'DE, NL, без данных последним');
    m.setNodeView(order: NodeSort.clean);
    expect(m.visibleNodes.map((n) => n.id), ['a1', 'b2', 'c3'], reason: 'чистый, грязный, без данных');
    expect(m.countries.map((e) => e.key), ['DE', 'NL']);
    m.setNodeView(order: NodeSort.ping);
    expect(m.visibleNodes.map((n) => n.id), ['b2', 'a1', 'c3'], reason: 'пинг: 100, 300, без данных последним');
    m.setNodeView(order: NodeSort.speed);
    expect(m.visibleNodes.map((n) => n.id), ['a1', 'b2', 'c3'], reason: 'скорость: быстрые первыми, без замера последним');
    await m.measureSpeed(m.nodes.last);
    expect(api.calls, contains('speed c3'));
    m.setCountry('NL');
    expect(m.visibleNodes.map((n) => n.id), ['a1']);
    m.setCountry(null);
    await m.connectNode(m.nodes.first);
    expect(api.calls, contains('connect a1'));
    m.dispose();
  });

  testWidgets('вкладка «Узлы»: выбор узла', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    await tester.tap(find.text('Узлы'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.text('one.example:443'), findsOneWidget);
    await tester.tap(find.text('one.example:443'));
    await tester.pump();
    expect(api.calls, contains('connect a1'));
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('вкладка «Узлы»: группы по странам сворачиваются по нажатию на заголовок', (tester) async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(VpnApp(model: m));
    await m.start();
    await tester.pump();
    await tester.tap(find.text('Узлы'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.text('one.example:443'), findsOneWidget);
    await tester.tap(find.text(countryName('NL')));
    await tester.pump();
    expect(find.text('one.example:443'), findsNothing, reason: 'группа свёрнута');
    expect(find.text('two.example:8443'), findsOneWidget, reason: 'остальные группы на месте');
    await tester.tap(find.text(countryName('NL')));
    await tester.pump();
    expect(find.text('one.example:443'), findsOneWidget, reason: 'группа развёрнута');
    m.dispose();
    await tester.pumpWidget(const SizedBox());
  });

  test('настройки: источники и WARP-проверки', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadSettings();
    expect(m.sources.single.name, 'main');
    expect(m.viaWarp, false);
    await m.setViaWarp(true);
    expect(m.viaWarp, true);
    await m.addSource('mine', 'https://example.org/sub');
    expect(m.sources.map((s) => s.name), ['main', 'mine']);
    await m.setSourceEnabled(m.sources.first, false);
    await m.removeSource(m.sources.last);
    expect(api.calls, containsAll(['set check.via_warp=1', 'addSource mine', 'src main false', 'rmSource mine']));
    m.dispose();
  });

  test('правила маршрутизации: правка, порядок, сохранение', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadRules();
    expect(m.rules, isEmpty);
    m.addRule(RouteRuleDto(title: 'a', action: 'direct', match: RouteMatch(domainSuffix: ['a.example'])));
    m.addRule(RouteRuleDto(title: 'b', action: 'block', match: RouteMatch(domain: ['b.example'], port: [443])));
    m.addRule(RouteRuleDto(title: 'c', match: RouteMatch(ruleSet: ['geosite-youtube'])));
    expect(m.rulesDirty, true);
    m.moveRule(2, 0); // c наверх
    expect(m.rules.map((r) => r.title), ['c', 'a', 'b']);
    m.moveRule(0, 3); // c вниз
    expect(m.rules.map((r) => r.title), ['a', 'b', 'c']);
    m.replaceRule(1, m.rules[1].copy()..enabled = false);
    m.removeRule(0);
    expect(await m.saveRules(), true);
    expect(m.rulesDirty, false);
    expect(api.calls, contains('putRules 2'));
    expect(m.rules.map((r) => r.title), ['b', 'c']);
    expect(m.rules.first.enabled, false);
    expect(m.rules.first.match.summary, contains('b.example'));
    m.dispose();
  });

  test('состояние иконки в трее: выключен, подключаюсь, подключён, проблема', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1), exitWaitSeconds: 2);
    expect(trayStateOf(m), TrayState.off, reason: 'до готовности демона — серая');
    await m.start();
    expect(trayStateOf(m), TrayState.off);
    api.probeOkAfter = 1000; // выход не отвечает
    await m.setOn(true);
    expect(m.vpn!.on, true);
    expect(trayStateOf(m), TrayState.problem, reason: 'VPN включён, но проверка выхода провалилась — красная');
    api.probeOkAfter = 0; // выход вернулся
    await m.checkExit();
    expect(trayStateOf(m), TrayState.on, reason: 'проверка прошла — зелёная');
    await m.setOn(false);
    expect(trayStateOf(m), TrayState.off);
    m.dispose();
  }, timeout: const Timeout(Duration(seconds: 90)));

  test('пакетный замер: выбор узлов и ход', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadNodes();
    m.setSelecting(true);
    m.toggleSelect('a1');
    m.toggleSelect('b2');
    expect(m.selected, {'a1', 'b2'});
    m.toggleSelect('a1');
    expect(m.selected, {'b2'});
    m.selectAllVisible();
    expect(m.selected.length, 3);
    await m.startMeasure(ping: true, speed: true);
    expect(api.calls.last, startsWith('measure '));
    expect(api.calls.last, contains('p=true e=false s=true'));
    expect(m.measure.running, true);
    await Future<void>.delayed(const Duration(milliseconds: 1300));
    expect(m.measure.running, false, reason: 'ход опрашивается раз в секунду; по окончании выбор сбрасывается');
    expect(m.selecting, false);
    expect(m.selected, isEmpty);
    expect(m.toast, contains('Замер завершён'));
    m.setSelecting(true);
    m.setSelecting(false);
    expect(m.selected, isEmpty);
    m.dispose();
  });

  test('свои серверы: добавление и удаление', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadOwn();
    expect(m.ownNodes.single.own, true);
    expect(await m.addOwn('vless://x@vps.example:443'), true);
    expect(api.ownTexts, ['vless://x@vps.example:443']);
    expect(m.toast, 'Добавлено: 1');
    await m.removeOwn(m.ownNodes.single);
    expect(api.calls, contains('rmOwn o1'));
    m.dispose();
  });

  test('настройки деградации и уведомлений', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadSettings();
    expect(m.degradeAction, 'notify');
    expect(m.notifyLevel, 'important');
    await m.setSetting('degrade.action', 'auto');
    await m.setSetting('notify.level', 'off');
    expect(m.degradeAction, 'auto');
    expect(m.notifyLevel, 'off');
    expect(api.calls, containsAll(['set degrade.action=auto', 'set notify.level=off']));
    m.dispose();
  });

  test('монитор: опрос только пока окно открыто, фильтр соединений', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    m.setMonitor(on: true, tab: 'conns');
    await Future<void>.delayed(const Duration(milliseconds: 100));
    expect(m.traffic.downNow, 4000);
    expect(m.conns.length, 1);
    expect(m.visibleConns.single.dest, 'example.com:443');
    m.setConnsView(closed: true);
    await Future<void>.delayed(const Duration(milliseconds: 100));
    expect(m.visibleConns.length, 2);
    m.setConnsView(search: 'dns');
    expect(m.visibleConns.single.route, 'direct');
    await m.closeConn(m.conns.first);
    expect(api.calls, contains('close c1'));
    m.setMonitor(tab: 'log');
    await Future<void>.delayed(const Duration(milliseconds: 100));
    expect(m.engineLines.length, 2);
    expect(m.eventList.single.kind, 'switch');
    m.setMonitor(on: false);
    m.dispose();
  });

  test('режим при выключенном VPN: мгновенно, без «подключения», последний выбор побеждает', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    var sawBusy = false;
    m.addListener(() => sawBusy = sawBusy || m.busy);
    final f1 = m.setMode('all');
    expect(m.vpn!.mode, 'all', reason: 'выбор виден сразу');
    final f2 = m.setMode('all-but-ru');
    await Future.wait([f1, f2]);
    expect(m.vpn!.mode, 'all-but-ru');
    expect(sawBusy, false, reason: 'смена режима выключенного VPN не должна показывать «подключаюсь/включаю»');
    m.dispose();
  });

  test('повторное нажатие во время подключения прерывает его и выключает VPN', () async {
    final api = FakeApi()..probeOkAfter = 1000000; // выход не появляется
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1), exitWaitSeconds: 30);
    await m.start();
    final f = m.setOn(true);
    await Future<void>.delayed(const Duration(milliseconds: 400));
    expect(m.busy, true);
    expect(m.connecting, true);
    await m.setOn(true); // ещё раз = прервать
    await f.timeout(const Duration(seconds: 5));
    expect(m.busy, false);
    expect(m.vpn!.on, false, reason: 'после прерывания VPN выключен');
    expect(m.toast, 'Подключение прервано');
    expect(api.calls.last, 'set on=false mode=null');
    m.dispose();
  });

  test('смена режима при включённом VPN: выбор во время смены не теряется', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1), exitWaitSeconds: 2);
    await m.start();
    await m.setOn(true);
    final f = m.setMode('all');
    final g = m.setMode('all-but-ru');
    await Future.wait([f, g]);
    expect(m.vpn!.mode, 'all-but-ru');
    expect(m.vpn!.on, true);
    m.dispose();
  });

  test('лучший узел, диагностика, статистика, названия и правка серверов', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.connectBest();
    expect(api.calls, contains('best'));
    expect(m.toast, contains('one.example'));
    await m.runDiagnostics();
    expect(m.diag!.ip, '1.2.3.4');
    expect(m.diag!.findings.single.level, 'ok');
    await m.loadDaily();
    expect(m.daily.single.vpnTotal, 55);
    await m.loadNodes();
    await m.setNodeLabel(m.nodes.first, 'Мой');
    expect(api.calls, contains('label a1 Мой'));
    expect(await m.editOwn(m.nodes.first, label: 'X', server: 'h', port: 1), true);
    expect(api.calls.last, startsWith('edit a1 label=X server=h port=1'));
    m.dispose();
  });

  test('резервная копия: перед применением показывается, что она изменит; отказ ничего не отправляет', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    final dir = Directory.systemTemp.createTempSync('bk');
    final path = '${dir.path}/evil.json';
    File(path).writeAsStringSync(jsonEncode({
      'version': 1,
      'settings': {'killswitch.enabled': '0'},
      'rules': [{}, {}],
      'sources': [{'name': 'x', 'url': 'https://evil.example/list'}],
      'own': [{'server': 'srv.example', 'port': 443}],
    }));
    String? shown;
    expect(await m.restoreBackup(path, confirm: (s) async { shown = s; return false; }), false);
    expect(api.lastBackup, isNull, reason: 'после отказа копия в демон не уходит');
    expect(shown, contains('killswitch.enabled = 0'));
    expect(shown, contains('evil.example'));
    expect(shown, contains('srv.example:443'));
    expect(shown, contains('будет 2'));
    expect(await m.restoreBackup(path, confirm: (s) async => true), true);
    expect(api.lastBackup, isNotNull);
    m.dispose();
  });

  test('резервная копия: сохранить в файл и восстановить', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    final dir = Directory.systemTemp.createTempSync('bk');
    final path = '${dir.path}/b.json';
    expect(await m.saveBackup(path), path);
    expect(File(path).readAsStringSync(), contains('degrade.action'));
    expect(await m.restoreBackup(path), true);
    expect(api.lastBackup!['version'], 1);
    expect(m.toast, contains('Восстановлено'));
    File('${dir.path}/bad.json').writeAsStringSync('[1,2]');
    expect(await m.restoreBackup('${dir.path}/bad.json'), false);
    dir.deleteSync(recursive: true);
    m.dispose();
  });

  test('список источников от автора: кнопка и переключатель', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.loadSettings();
    expect(m.otaAuto, true, reason: 'по умолчанию включено');
    await m.setOtaAuto(false);
    expect(api.calls, contains('set ota.sources=0'));
    expect(m.otaAuto, false);
    await m.updateOtaSources();
    expect(api.calls, contains('ota sources'));
    expect(m.toast, contains('версия 3'));
    m.dispose();
  });

  test('раздельное туннелирование: одна запись, ошибка перезапуска не теряется', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    expect(await m.setTunApps('exclude', ['ru.vk.vkx', 'com.example.b']), isNull);
    expect(api.calls.where((c) => c.startsWith('tun apps')).toList(), ['tun apps exclude ru.vk.vkx,com.example.b']);
    expect(m.settings['tun.apps_mode'], 'exclude');
    expect(m.toast, 'Применено');
    api.tunAppsRestartError = 'нет разрешения на VPN';
    final err = await m.setTunApps('off', []);
    expect(err, contains('нет разрешения на VPN'));
    expect(m.toastError, true);
    m.dispose();
  });

  test('WARP: регистрация и загрузка аккаунта из файла', () async {
    final api = FakeApi();
    final m = AppModel(api, runner: AliveRunner(api), pollEvery: const Duration(hours: 1));
    await m.start();
    await m.warpRegister();
    expect(api.calls, contains('warp register replace=false'));
    expect(m.toast, 'WARP настроен');
    expect(m.warpBusy, false);
    final dir = Directory.systemTemp.createTempSync('warp');
    File('${dir.path}/a.json').writeAsStringSync('{"private_key":"k","peer_public_key":"p","addr_v4":"10.0.0.2"}');
    await m.warpImportFile('${dir.path}/a.json', replace: true);
    expect(api.calls, contains('warp import 3 replace=true'));
    File('${dir.path}/bad.json').writeAsStringSync('[1]');
    await m.warpImportFile('${dir.path}/bad.json');
    expect(m.toastError, true, reason: 'не словарь — ошибка, а не вызов демона');
    expect(api.calls.where((c) => c.startsWith('warp import')).length, 1);
    dir.deleteSync(recursive: true);
    m.dispose();
  });

  test('шаблоны правил корректны', () {
    final titles = rulePresets.map((p) => p.title).toSet();
    expect(titles.length, rulePresets.length, reason: 'названия уникальны: по ним определяется «уже добавлено»');
    for (final p in rulePresets) {
      expect(['proxy', 'direct'], contains(p.action));
      if (p.build != null) {
        // набор из нескольких правил: названия начинаются с названия шаблона (по нему определяется «уже добавлено»), условия не пустые
        final rules = p.rules();
        expect(rules, isNotEmpty, reason: p.title);
        for (final r in rules) {
          expect(r.title, startsWith(p.title));
        }
        continue;
      }
      expect(p.suffixes, isNotEmpty);
      for (final d in p.suffixes) {
        expect(d, matches(RegExp(r'^[a-z0-9.-]+\.[a-z]{2,}$')), reason: p.title);
      }
    }
  });
}
