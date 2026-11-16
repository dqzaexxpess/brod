// трей (tray.dart): состояние значка по состоянию приложения, файлы значков, обработка пунктов меню и
// закрытия окна. Плагины window_manager подменены на уровне канала, настоящего окна и трея нет. Выход (quit) не вызывается: он завершает процесс.
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:tray_manager/tray_manager.dart' show MenuItem;
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/prefs.dart';
import 'package:brod/src/tray.dart';

class _Api implements DaemonApi {
  final calls = <String>[];
  VpnState vpnNow = const VpnState(on: false, mode: 'all');

  @override
  Future<VpnState> vpn() async => vpnNow;

  @override
  Future<VpnState> setVpn({bool? on, String? mode, String? autoconnect}) async {
    calls.add('setVpn:on=$on:mode=$mode');
    vpnNow = VpnState(on: on ?? vpnNow.on, mode: mode ?? vpnNow.mode);
    return vpnNow;
  }

  @override
  Future<NodeDto> connectBest() async {
    calls.add('connectBest');
    throw ApiException(409, 'нет живых узлов');
  }

  @override
  Future<DaemonStatus> status() async => const DaemonStatus(version: 't', nodes: 0, present: 0, degraded: false);

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

MenuItem _mi(String? key) => MenuItem(key: key);

AppModel _model(_Api api, {bool ready = true, VpnState? vpn}) {
  final m = AppModel(api, pollEvery: const Duration(hours: 1));
  if (ready) m.phase = Phase.ready;
  m.vpn = vpn;
  return m;
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  group('trayStateOf', () {
    final api = _Api();
    test('нет ответа демона или фаза не «готово» — серый', () {
      expect(trayStateOf(_model(api, vpn: null)), TrayState.off);
      expect(trayStateOf(_model(api, ready: false, vpn: const VpnState(on: true, mode: 'all'))), TrayState.off);
    });

    test('VPN выключен — серый, идёт переключение — жёлтый (даже при выключенном)', () {
      expect(trayStateOf(_model(api, vpn: const VpnState(on: false, mode: 'all'))), TrayState.off);
      final m = _model(api, vpn: const VpnState(on: false, mode: 'all'))..busy = true;
      expect(trayStateOf(m), TrayState.connecting);
    });

    test('включён — зелёный; деградация или неотвечающий выход — красный', () {
      const on = VpnState(on: true, mode: 'all');
      expect(trayStateOf(_model(api, vpn: on)), TrayState.on);
      final bad = _model(api, vpn: on)..probe = const ProbeResult(ok: false);
      expect(trayStateOf(bad), TrayState.problem);
      final ok = _model(api, vpn: on)..probe = const ProbeResult(ok: true, latencyMs: 80);
      expect(trayStateOf(ok), TrayState.on);
      final degraded = _model(api, vpn: on)..status = const DaemonStatus(version: 't', nodes: 1, present: 1, degraded: true);
      expect(trayStateOf(degraded), TrayState.problem);
    });
  });

  test('значки: для каждого состояния есть файл PNG в assets, на Windows — .ico', () {
    for (final s in TrayState.values) {
      final png = trayIcons[s]!;
      expect(File(png).existsSync(), isTrue, reason: '$png должен лежать в app/');
      expect(trayIconPath(s), Platform.isWindows ? png.replaceFirst('.png', '.ico') : png);
    }
    expect(trayIcons.length, TrayState.values.length);
  });

  group('TrayController', () {
    final windowCalls = <String>[];

    setUp(() {
      windowCalls.clear();
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(const MethodChannel('window_manager'), (call) async {
        windowCalls.add(call.method);
        return call.method.startsWith('is') ? false : null; // isMinimized и подобные отвечают bool
      });
    });
    tearDown(() => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(const MethodChannel('window_manager'), null));

    Future<void> settle() => Future<void>.delayed(const Duration(milliseconds: 50));

    test('пункты меню: режим, лучший узел, выключение доходят до модели', () async {
      final api = _Api();
      final m = _model(api, vpn: const VpnState(on: false, mode: 'all'));
      final t = TrayController(m, Prefs());
      t.onTrayMenuItemClick(_mi('mode:all-but-ru'));
      await settle();
      expect(api.calls, contains('setVpn:on=null:mode=all-but-ru'));
      t.onTrayMenuItemClick(_mi('best'));
      await settle();
      expect(api.calls, contains('connectBest'));
      expect(m.toast, contains('нет живых узлов'));
      m.vpn = const VpnState(on: true, mode: 'all');
      api.vpnNow = const VpnState(on: true, mode: 'all');
      t.onTrayMenuItemClick(_mi('toggle'));
      await settle();
      expect(api.calls, contains('setVpn:on=false:mode=null'));
      t.onTrayMenuItemClick(_mi('неизвестный'));
      t.onTrayMenuItemClick(_mi(null));
      await settle();
    });

    test('«Открыть окно» и значок трея показывают окно и переводят модель в передний план', () async {
      final m = _model(_Api(), vpn: const VpnState(on: false, mode: 'all'));
      final t = TrayController(m, Prefs());
      m.setForeground(false);
      t.onTrayMenuItemClick(_mi('open'));
      await settle();
      expect(windowCalls, containsAll(['show', 'focus']));
      m.setForeground(false);
      windowCalls.clear();
      t.onTrayIconMouseDown();
      await settle();
      expect(windowCalls, contains('show'));
    });

    test('закрытие окна при closeToTray прячет окно, а не выходит', () async {
      final t = TrayController(_model(_Api(), vpn: null), Prefs(closeToTray: true));
      t.onWindowClose();
      await settle();
      expect(windowCalls, contains('hide'));
    });
  });
}
