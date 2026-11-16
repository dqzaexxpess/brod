import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart' show kDebugMode;

import 'api.dart';
import 'daemon_errors.dart';
import 'platform_paths.dart';
import 'timing.dart';

/// Запуск и остановка демона `brodd`. Если демон уже работает (отвечает сокет) — используем его и при выходе не трогаем.
class DaemonRunner {
  DaemonRunner(this.api);

  final DaemonApi api;
  Process? _proc;

  bool get ownsDaemon => _proc != null;

  /// Файл-признак «правила киллсвитча стоят» (его пишет демон, см. killswitch.Marker).
  String get killswitchMarker => killswitchMarkerPath(Platform.environment, windows: Platform.isWindows);

  static String get _uid {
    try {
      return Process.runSync('id', ['-u']).stdout.toString().trim();
    } catch (_) {
      return '0';
    }
  }

  bool killswitchMarkerExists() {
    if (File(killswitchMarker).existsSync()) return true;
    // прежний признак в общем /tmp (демон старой версии): считается только свой файл, чужой с тем же именем — нет
    final uid = Platform.isWindows ? '' : _uid;
    final old = legacyKillswitchMarkerPath(Platform.environment, windows: Platform.isWindows, tmpDir: Directory.systemTemp.path, uid: uid);
    return old != null && fileOwnedByUid(old, uid);
  }

  /// Снимает правила киллсвитча командой самого демона (у него есть право на сеть). Возвращает сообщение об ошибке или null.
  Future<String?> releaseKillswitch() async {
    final manual = Platform.isWindows
        ? 'Вручную, в консоли администратора: brodctl killswitch release (или перезагрузка)'
        : 'Вручную: sudo nft delete table inet brod_ks';
    final bin = findBinary();
    if (bin == null) return 'Не найден brodd. $manual';
    try {
      final r = await Process.run(bin, const ['--release-killswitch']).timeout(const Duration(seconds: 20));
      if (r.exitCode != 0) return Platform.isWindows ? await _releaseViaService(manual) : 'Не удалось снять блокировку: ${r.stderr.toString().trim()}. $manual';
    } catch (e) {
      return Platform.isWindows ? await _releaseViaService(manual) : 'Не удалось снять блокировку: $e. $manual';
    }
    return null;
  }

  /// Windows без прав администратора: фильтры WFP снимает только демон-служба (LocalSystem), поэтому служба запускается заново и демона просят
  /// снять правила по API. Запускать и останавливать службу обычному пользователю разрешает установщик (SDDL).
  Future<String?> _releaseViaService(String manual) async {
    final a = api;
    if (a is! UnixApi) return 'Не удалось снять блокировку. $manual';
    try {
      if (!await alive() && !await _startWindowsTask()) return 'Не удалось запустить службу «$windowsService». $manual';
      for (var i = 0; i < 60 && !await alive(); i++) {
        await Future<void>.delayed(const Duration(milliseconds: 500));
      }
      if (!await alive()) return 'Служба «$windowsService» не ответила за 30 с. $manual';
      await a.releaseKillswitch();
    } catch (e) {
      return 'Не удалось снять блокировку: $e. $manual';
    }
    return null;
  }

  /// Где искать brodd: переменная BRODD_PATH, рядом с приложением, ~/.local/bin, каталог разработки (../bin/brodd от app/).
  static String? findBinary() {
    final cands = broddCandidates(Platform.environment,
        exeDir: File(Platform.resolvedExecutable).parent.path, cwd: Directory.current.path, windows: Platform.isWindows, dev: kDebugMode);
    for (final c in cands) {
      if (File(c).existsSync()) return File(c).resolveSymbolicLinksSync();
    }
    return null;
  }

  Future<bool> alive() async {
    try {
      await api.status();
      return true;
    } catch (_) {
      return false;
    }
  }

  /// Имя задачи планировщика Windows: так демон запускала установка до появления службы (install.ps1 её удаляет).
  static const windowsTask = 'Brod daemon';

  /// Имя службы Windows (создаёт deploy/windows/install.ps1).
  static const windowsService = 'Brod';

  /// Журнал самого демона (brodd.log в каталоге состояния).
  String get logPath => '${stateDirPath(Platform.environment, windows: Platform.isWindows)}${Platform.pathSeparator}brodd.log';

  /// Хвост журнала демона для экрана ошибки; null — файла нет.
  String? logTail([int n = 8]) {
    try {
      final f = File(logPath);
      final len = f.lengthSync();
      final raf = f.openSync();
      try {
        const chunk = 16 * 1024;
        raf.setPositionSync(len > chunk ? len - chunk : 0);
        return tailLines(String.fromCharCodes(raf.readSync(len > chunk ? chunk : len)), n);
      } finally {
        raf.closeSync();
      }
    } catch (_) {
      return null;
    }
  }

  /// Windows: заново запустить задачу планировщика, когда демон умер (падение, отключение питания). Не Windows или задачи нет — false.
  Future<bool> restartService() => _startWindowsTask();

  /// Windows: демону для туннеля нужны права администратора, а приложение работает без них, поэтому демон держит служба «Brod»
  /// (запускается вместе с системой от LocalSystem; установщик разрешает обычным пользователям её запуск и остановку). Если службы нет
  /// (установка прежней версией), запускается задача планировщика «Brod daemon». Возвращает true, если служба или задача есть и запущена.
  Future<bool> _startWindowsTask() async {
    if (!Platform.isWindows) return false;
    try {
      final q = await Process.run('sc.exe', ['query', windowsService]);
      if (q.exitCode == 0) {
        if ('${q.stdout}'.contains('RUNNING')) return true; // уже работает: демон может ещё подниматься, ждёт вызывающий
        final r = await Process.run('sc.exe', ['start', windowsService]);
        return r.exitCode == 0 || r.exitCode == 1056; // 1056 — уже запущена
      }
      final t = await Process.run('schtasks', ['/Query', '/TN', windowsTask]);
      if (t.exitCode != 0) return false;
      final r = await Process.run('schtasks', ['/Run', '/TN', windowsTask]);
      return r.exitCode == 0;
    } catch (_) {
      return false;
    }
  }

  /// Гарантирует работающий демон. Возвращает сообщение об ошибке или null.
  Future<String?> ensureRunning() async {
    if (await alive()) return null;
    if (await _startWindowsTask()) {
      for (var i = 0; i < 60; i++) {
        if (await alive()) return null; // демон принадлежит службе, при выходе из приложения его не трогаем
        await Future<void>.delayed(const Duration(milliseconds: 500));
      }
      final tail = logTail();
      return 'Демон (служба «$windowsService») не ответил за 30 с.${tail == null || tail.isEmpty ? '' : '\n\nЖурнал демона (brodd.log):\n$tail'}';
    }
    final bin = findBinary();
    if (bin == null) return 'Не найден brodd (задайте BRODD_PATH или положите его рядом с приложением)';
    try {
      _proc = await Process.start(bin, const [], mode: ProcessStartMode.normal);
    } catch (e) {
      return 'Не удалось запустить brodd: $e';
    }
    // журнал демона нам не нужен, но поток надо читать, иначе процесс упрётся в буфер
    _proc!.stdout.drain<void>();
    _proc!.stderr.drain<void>();
    unawaited(_proc!.exitCode.then((_) => _proc = null));
    for (var i = 0; i < 60; i++) {
      if (await alive()) return null;
      if (_proc == null) {
        // наш brodd завершился: чаще всего каталог состояния уже занят другим brodd (параллельный запуск, служба), который ещё поднимается;
        // ждём его, а не сообщаем об ошибке (демон тогда чужой, при выходе мы его не трогаем)
        for (var j = 0; j < 40; j++) {
          await Future<void>.delayed(const Duration(milliseconds: 500));
          if (await alive()) return null;
        }
        return 'brodd завершился сразу после запуска (проверьте, что он не запущен ещё где-то и есть права на каталог состояния)';
      }
      await Future<void>.delayed(const Duration(milliseconds: 500));
    }
    return 'brodd не ответил за 30 с';
  }

  /// Останавливает демон, только если его запустили мы (SIGTERM, до 12 с, затем SIGKILL).
  Future<void> stop() async {
    final p = _proc;
    if (p == null) return;
    final sw = Stopwatch()..start();
    p.kill(ProcessSignal.sigterm);
    try {
      await p.exitCode.timeout(const Duration(seconds: 12));
      logTiming('остановка демона: SIGTERM → выход', {'ожидание выхода': sw.elapsed});
    } on TimeoutException {
      p.kill(ProcessSignal.sigkill);
      final tTerm = sw.elapsed;
      await releaseKillswitch(); // жёстко убитый демон не снимет правила сам: закрытое приложение не должно оставлять сеть заблокированной
      logTiming('остановка демона: таймаут, SIGKILL', {'ожидание SIGTERM': tTerm, 'снятие блокировки': sw.elapsed - tTerm});
    }
    _proc = null;
  }
}
