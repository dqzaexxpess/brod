import 'dart:io';
import 'dart:ui';

import 'package:flutter/material.dart';

import 'src/android.dart';
import 'src/api.dart';
import 'src/model.dart';
import 'src/prefs.dart';
import 'src/setup/setup_args.dart';
import 'src/setup/setup_main.dart';
import 'src/single.dart';
import 'src/tray.dart';
import 'src/ui.dart';
import 'src/win_notices.dart';

Future<void> main(List<String> args) async {
  WidgetsFlutterBinding.ensureInitialized();
  if (Platform.isAndroid) return runAndroid();
  // установка и удаление (setup.exe запускает Brod.exe --setup, «Приложения» — Brod.exe --uninstall): отдельное окно, без демона, трея и единственного экземпляра
  final setup = parseSetupArgs(args);
  if (setup.isSetup) return runSetup(setup);
  final api = UnixApi(UnixApi.defaultSocket());
  final model = AppModel(api);
  final prefs = Prefs.load();
  final tray = TrayController(model, prefs);
  // один экземпляр: повторный запуск только показывает окно уже работающего приложения
  final startedAt = DateTime.now();
  final single = await SingleInstance.acquire(() {
    // повторный запуск в первые секунды скрытого старта — это восстановление сеанса или второй автозапуск, а не желание пользователя открыть окно
    if (prefs.startHidden && DateTime.now().difference(startedAt) < const Duration(seconds: 20)) return;
    tray.showWindow();
  });
  if (single == null) exit(0);
  tray.onQuitHook = single.close;
  model.onQuitForInstall = tray.quit; // после запуска установки обновления приложение закрывается, помощник запускает его снова
  try {
    await tray.init();
  } catch (e) {
    // без трея (нет поддержки в окружении) приложение работает как обычное окно: крестик закрывает его
    stderr.writeln('трей недоступен: $e');
    prefs.closeToTray = false;
  }
  // при закрытии окна гасим демон, если запускали его сами (если демон уже работал — не трогаем)
  AppLifecycleListener(onExitRequested: () async {
    await model.shutdown();
    return AppExitResponse.exit;
  });
  // SIGTERM/SIGINT (выключение сессии, kill) — тоже штатный выход: демон, запущенный нами, не должен остаться сиротой
  for (final sig in [ProcessSignal.sigterm, ProcessSignal.sigint]) {
    sig.watch().listen((_) => tray.quit());
  }
  runApp(VpnApp(model: model, prefs: prefs, onQuit: tray.quit));
  model.start();
  // Windows: уведомления демона (служба в сеансе 0) показывает приложение
  if (Platform.isWindows) {
    WinNotices(poll: api.pollNotices, action: api.noticeAction, onOpen: tray.showWindow).start().catchError((Object e) {
      stderr.writeln('уведомления Windows недоступны: $e');
    });
  }
}
