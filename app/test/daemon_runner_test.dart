// T-9: DaemonRunner (daemon.dart, раньше покрытие 10%): то, что можно проверить без запуска настоящего brodd. Запуск процесса
// (ensureRunning без живого демона), снятие блокировки (releaseKillswitch) и служба Windows тут намеренно не вызываются: они
// запустили бы найденный на машине brodd или sc.exe.
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/daemon.dart';

class _Api implements DaemonApi {
  _Api(this.up);

  bool up;
  int statusCalls = 0;

  @override
  Future<DaemonStatus> status() async {
    statusCalls++;
    if (!up) throw const SocketException('нет сокета');
    return const DaemonStatus(version: 't', nodes: 0, present: 0, degraded: false);
  }

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  test('alive: отвечает status — жив, ошибка сокета — нет', () async {
    expect(await DaemonRunner(_Api(true)).alive(), isTrue);
    expect(await DaemonRunner(_Api(false)).alive(), isFalse);
  });

  test('ensureRunning при живом демоне ничего не запускает и не становится его владельцем', () async {
    final api = _Api(true);
    final r = DaemonRunner(api);
    expect(await r.ensureRunning(), isNull);
    expect(api.statusCalls, 1);
    expect(r.ownsDaemon, isFalse, reason: 'чужого (службы) демона при выходе не трогаем');
  });

  test('stop без собственного процесса — безвредный пустой вызов', () async {
    final r = DaemonRunner(_Api(true));
    await r.stop();
    expect(r.ownsDaemon, isFalse);
  });

  test('restartService вне Windows — false без запуска команд', () async {
    if (Platform.isWindows) return;
    expect(await DaemonRunner(_Api(false)).restartService(), isFalse);
  });

  test('имена службы и задачи Windows согласованы с установщиком', () {
    expect(DaemonRunner.windowsService, 'Brod');
    expect(DaemonRunner.windowsTask, 'Brod daemon');
  });

  test('признак киллсвитча и журнал демона: пути и безопасное чтение', () {
    final r = DaemonRunner(_Api(false));
    expect(r.killswitchMarker, Platform.isWindows ? endsWith(r'Brod\killswitch.active') : contains('brod-killswitch'));
    expect(r.killswitchMarker.endsWith('.active'), isTrue);
    expect(r.logPath, endsWith('brodd.log'));
    // чтение журнала не бросает (файла может не быть); существование признака — просто bool
    expect(() => r.logTail(), returnsNormally);
    expect(r.killswitchMarkerExists(), isA<bool>());
  });
}
