import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';

import 'setup_app.dart';
import 'setup_args.dart';
import 'setup_runner.dart';

String _join(String a, String b) => '$a${Platform.pathSeparator}$b';

Future<void> _copyDir(Directory from, Directory to) async {
  await to.create(recursive: true);
  await for (final e in from.list(followLinks: false)) {
    final name = e.uri.pathSegments.where((s) => s.isNotEmpty).last;
    if (e is Directory) {
      await _copyDir(e, Directory(_join(to.path, name)));
    } else if (e is File) {
      await e.copy(_join(to.path, name));
    }
  }
}

class _CloseListener with WindowListener {
  _CloseListener(this.onClose);

  final void Function() onClose;

  @override
  void onWindowClose() => onClose();
}

/// Помощник очистки запускается сразу, а не при выходе: он ждёт завершения этого процесса (при любом способе выхода: «Готово», крестик,
/// сбой) и стирает временную копию, повторяя попытки, пока файлы заняты. Ход пишется в %TEMP%\brod-cleanup.log (при успехе файл стирается:
/// остаётся только при неудаче, по нему видно причину). Запасной путь — запись RunOnce: если помощник не отработал, копию уберёт
/// следующий вход в систему.
Future<void> _spawnCleanup(String dir) async {
  final path = dir.replaceAll("'", "''");
  // рабочий каталог помощника — вне удаляемой папки: процесс с текущим каталогом внутри держит её, и Remove-Item не может её стереть
  final home = Platform.environment['SystemRoot'] ?? Directory.systemTemp.path;
  final script = "Set-Location -LiteralPath '$home'; \$log = Join-Path \$env:TEMP 'brod-cleanup.log'; "
      "function L(\$m) { Add-Content -LiteralPath \$log -Value ((Get-Date -Format o) + ' ' + \$m) }; "
      "Set-Content -LiteralPath \$log -Value ((Get-Date -Format o) + ' ждём процесс $pid'); "
      "Wait-Process -Id $pid -ErrorAction SilentlyContinue; L 'процесс завершён'; "
      "for (\$i = 0; \$i -lt 30; \$i++) { "
      "try { Remove-Item -Recurse -Force -LiteralPath '$path' -ErrorAction Stop } catch { L ('ошибка: ' + \$_.Exception.Message) }; "
      "if (-not (Test-Path -LiteralPath '$path')) { Remove-Item -LiteralPath \$log -Force -ErrorAction SilentlyContinue; break }; "
      "Start-Sleep 1 }";
  // Запуск через `cmd /c start`, как у перезапуска приложения после обновления (model.dart): PowerShell, запущенный напрямую в режиме
  // detached (DETACHED_PROCESS, без консоли), завершается сразу и ничего не выполняет — помощник не стартовал, журнала не было
  // (проверено на Windows 11 отдельным повтором: detached и detachedWithStdio — скрипт не выполняется, normal — умирает вместе
  // с родителем, через start — копия стёрта). Скрипт — в -EncodedCommand, чтобы cmd не разбирал спецсимволы пути.
  final utf16 = <int>[for (final u in script.codeUnits) ...[u & 0xff, u >> 8]];
  await Process.start('cmd.exe', ['/c', 'start', '""', '/min', 'powershell.exe', '-NoProfile', '-WindowStyle', 'Hidden', '-EncodedCommand', base64.encode(utf16)],
      workingDirectory: home, mode: ProcessStartMode.detached);
  try {
    await Process.run('reg', ['add', r'HKCU\Software\Microsoft\Windows\CurrentVersion\RunOnce', '/v', 'BrodCleanup', '/t', 'REG_SZ', '/d', 'cmd /c rd /s /q "$dir"', '/f']);
  } on ProcessException {
    // реестр недоступен: остаётся помощник и очистка при следующем запуске установщика
  }
}

/// Копии прежних удалений и распаковок, оставшиеся от сбоев, стираются при следующем запуске (старше 10 минут, кроме своей папки).
void _removeStaleTemp(String own) {
  try {
    final now = DateTime.now();
    for (final e in Directory.systemTemp.listSync(followLinks: false)) {
      final name = e.uri.pathSegments.where((s) => s.isNotEmpty).last;
      if (e is! Directory || !(name.startsWith('brod-uninstall-') || name.startsWith('brod-setup-')) || e.path == own) continue;
      if (own.startsWith(e.path)) continue;
      if (now.difference(e.statSync().modified) > const Duration(minutes: 10)) {
        try {
          e.deleteSync(recursive: true);
        } on FileSystemException {
          // занята работающим процессом: останется до следующего раза
        }
      }
    }
  } on FileSystemException {
    // каталог временных файлов недоступен: очистка не обязательна
  }
}

/// Окно установки и удаления: тот же `Brod.exe`, запущенный с `--setup` (из архива, который распаковал setup.exe) или `--uninstall`
/// (из «Параметры → Приложения»). Удаление сначала копирует программу во временную папку и работает оттуда: из своей же папки
/// запущенный Brod.exe удалить себя не может.
Future<void> runSetup(SetupArgs a) async {
  WidgetsFlutterBinding.ensureInitialized();
  final exeDir = File(Platform.resolvedExecutable).parent;

  if (a.mode == SetupMode.uninstall && !a.fromTemp) {
    final tmp = Directory(_join(Directory.systemTemp.path, 'brod-uninstall-${DateTime.now().millisecondsSinceEpoch}'));
    await _copyDir(exeDir, tmp);
    await Process.start(_join(tmp.path, 'Brod.exe'), ['--uninstall', '--from-temp', '--install-dir', a.installDir.isEmpty ? exeDir.path : a.installDir],
        mode: ProcessStartMode.detached);
    exit(0);
  }

  _removeStaleTemp(exeDir.path);
  if (a.fromTemp) {
    // сам процесс тоже не должен стоять в удаляемой папке
    try {
      Directory.current = Platform.environment['SystemRoot'] ?? Directory.systemTemp.path;
    } on FileSystemException {
      // остаёмся где были: помощник всё равно запускается с другим каталогом
    }
    await _spawnCleanup(exeDir.path);
  }

  final install = a.mode == SetupMode.install;
  final dir = a.installDir.isNotEmpty ? a.installDir : (install ? _join(Platform.environment['ProgramFiles'] ?? r'C:\Program Files', 'Brod') : exeDir.path);

  await windowManager.ensureInitialized();
  await windowManager.waitUntilReadyToShow(
    const WindowOptions(size: Size(560, 640), center: true, title: 'Brod', titleBarStyle: TitleBarStyle.normal),
    () async {
      await windowManager.setResizable(false);
      await windowManager.setMaximizable(false);
      await windowManager.setTitle(install ? 'Установка Brod' : 'Удаление Brod');
      await windowManager.show();
    },
  );

  // закрытие окна крестиком тоже выход: без этого процесс завершался, не дойдя до очистки
  windowManager.addListener(_CloseListener(() => exit(0)));
  await windowManager.setPreventClose(true);

  void leave(int code) => exit(code);

  runApp(MaterialApp(
    debugShowCheckedModeBanner: false,
    title: 'Brod',
    theme: setupTheme(),
    home: SetupWizard(
      mode: a.mode,
      runner: WindowsSetupRunner(scriptsDir: exeDir.path),
      installDir: dir,
      onExit: (c) => leave(c),
      launchApp: (d) => Process.start(_join(d, 'Brod.exe'), const [], workingDirectory: d, mode: ProcessStartMode.detached),
    ),
  ));
}
