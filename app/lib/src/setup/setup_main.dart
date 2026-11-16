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

  void leave(int code) {
    if (a.fromTemp) {
      // временную копию стирает отдельный процесс после выхода этого
      Process.start('powershell.exe', ['-NoProfile', '-WindowStyle', 'Hidden', '-Command', "Start-Sleep 3; Remove-Item -Recurse -Force -LiteralPath '${exeDir.path.replaceAll("'", "''")}'"],
          mode: ProcessStartMode.detached);
    }
    exit(code);
  }

  runApp(MaterialApp(
    debugShowCheckedModeBanner: false,
    title: 'Brod',
    theme: setupTheme(),
    home: SetupWizard(
      mode: a.mode,
      runner: WindowsSetupRunner(scriptsDir: exeDir.path),
      installDir: dir,
      onExit: leave,
      launchApp: (d) => Process.start(_join(d, 'Brod.exe'), const [], workingDirectory: d, mode: ProcessStartMode.detached),
    ),
  ));
}
