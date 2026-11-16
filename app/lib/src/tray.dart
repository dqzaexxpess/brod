import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:screen_retriever/screen_retriever.dart';
import 'package:tray_manager/tray_manager.dart';
import 'package:window_manager/window_manager.dart';

import 'api.dart';
import 'brief.dart';
import 'model.dart';
import 'prefs.dart';
import 'timing.dart';
import 'window_geometry.dart';

/// Состояние иконки в трее: выключен (серая), подключаюсь (жёлтая), подключён (зелёная), проблема (красная).
enum TrayState { off, connecting, on, problem }

const trayIcons = <TrayState, String>{
  TrayState.off: 'assets/tray_off.png',
  TrayState.connecting: 'assets/tray_connecting.png',
  TrayState.on: 'assets/tray_on.png',
  TrayState.problem: 'assets/tray_problem.png',
};

/// Файл значка трея: tray_manager на Windows принимает только .ico, на остальных платформах — PNG.
String trayIconPath(TrayState s) {
  final png = trayIcons[s]!;
  return Platform.isWindows ? png.replaceFirst(RegExp(r'\.png$'), '.ico') : png;
}

/// Состояние иконки по состоянию приложения: «проблема» — VPN включён, но выход не отвечает или активный узел деградирует.
TrayState trayStateOf(AppModel m) {
  final v = m.vpn;
  if (m.phase != Phase.ready || v == null) return TrayState.off;
  if (m.busy) return TrayState.connecting;
  if (!v.on) return TrayState.off;
  if (m.status?.degraded == true || (m.probe != null && !m.probe!.ok)) return TrayState.problem;
  return TrayState.on;
}

/// Иконка в трее и закрытие окна. Приложение живёт в фоне: окно прячется, демон и VPN работают.
class TrayController with TrayListener, WindowListener {
  TrayController(this.model, this.prefs);

  final AppModel model;
  final Prefs prefs;
  bool _quitting = false;
  Future<void> Function()? onQuitHook; // освободить ресурсы перед выходом (сокет единственного экземпляра)
  String _sig = '';

  Future<void> init() async {
    await windowManager.ensureInitialized();
    await windowManager.setPreventClose(true);
    windowManager.addListener(this);
    await _restoreBounds();
    trayManager.addListener(this);
    await trayManager.setIcon(trayIconPath(TrayState.off));
    await _tip('Brod');
    try {
      await trayManager.setTitle('Brod');
    } catch (_) {}
    model.backgroundPoll = const Duration(seconds: 5); // окно спрятано или свёрнуто: опрос реже, но значок трея не должен отставать
    model.addListener(_refreshMenu);
    await _refreshMenu();
    prefs.addListener(_menuTheme);
    await _menuTheme();
    // одна настройка для любого запуска — и ручного, и при входе в систему (Autostart)
    if (prefs.startHidden) {
      await windowManager.hide();
      model.setForeground(false);
    }
  }

  // ---- положение и размер окна ----
  Timer? _boundsTimer;

  Future<void> _restoreBounds() async {
    final saved = prefs.windowBounds;
    if (saved == null) return;
    try {
      final displays = await screenRetriever.getAllDisplays();
      final rects = [
        for (final d in displays)
          if (d.visibleSize != null) (d.visiblePosition ?? const Offset(0, 0)) & d.visibleSize!,
      ];
      final r = restorableBounds(saved, rects);
      if (r != null) await windowManager.setBounds(r);
    } catch (_) {} // без сведений о мониторах окно остаётся там, где его поставила система
  }

  // окно двигают и растягивают непрерывно: пишем через паузу, когда оно остановилось
  void _boundsChanged() {
    _boundsTimer?.cancel();
    _boundsTimer = Timer(const Duration(milliseconds: 600), () async {
      try {
        if (!await windowManager.isVisible() || await windowManager.isMaximized() || await windowManager.isMinimized() || await windowManager.isFullScreen()) return;
        prefs.setWindowBounds(await windowManager.getBounds());
      } catch (_) {}
    });
  }

  @override
  void onWindowMoved() => _boundsChanged();

  @override
  void onWindowResized() => _boundsChanged();

  @override
  void onWindowMove() => _boundsChanged();

  @override
  void onWindowResize() => _boundsChanged();

  // значок окна (панель задач) в цвет состояния; канал реализован в linux/runner/my_application.cc
  static const _winChannel = MethodChannel('brod/window');

  Future<void> _windowIcon(String asset) async {
    if (Platform.isWindows) return; // там значок окна задаёт runner (win32_window.cpp), цвет по состоянию не меняется
    try {
      final dir = File(Platform.resolvedExecutable).parent.path;
      await _winChannel.invokeMethod('setIcon', '$dir/data/flutter_assets/$asset');
    } catch (_) {}
  }

  // Windows: системное меню трея в теме приложения (тёмная/светлая/как в системе); реализация в windows/runner/menu_theme.cpp.
  // В Linux меню рисует оболочка рабочего стола, и оно следует её теме.
  String? _menuMode;

  Future<void> _menuTheme() async {
    if (!Platform.isWindows || prefs.themeMode == _menuMode) return;
    _menuMode = prefs.themeMode;
    try {
      await _winChannel.invokeMethod('setMenuTheme', prefs.themeMode);
    } catch (_) {}
  }

  // подсказка иконки есть не во всех окружениях (в Linux плагин её не умеет) — без неё трей работает
  Future<void> _tip(String t) async {
    try {
      await trayManager.setToolTip(t);
    } catch (_) {}
  }

  Future<void> showWindow() async {
    await windowManager.show();
    await windowManager.focus();
    model.setForeground(true);
  }

  Future<void> quit() async {
    if (_quitting) return;
    _quitting = true;
    final sw = Stopwatch()..start();
    await model.shutdown(); // демон гасим, только если запускали сами
    final tDaemon = sw.elapsed;
    try {
      await onQuitHook?.call();
    } catch (_) {}
    final tHook = sw.elapsed - tDaemon;
    try {
      await trayManager.destroy();
    } catch (_) {}
    logTiming('«Выйти»', {'демон': tDaemon, 'ловушка выхода': tHook, 'значок трея': sw.elapsed - tDaemon - tHook});
    exit(0);
  }

  Future<void> _refreshMenu() async {
    final v = model.vpn;
    final on = v?.on ?? false;
    final busy = model.busy;
    final ts = trayStateOf(model);
    final brief = on ? briefLine(v, model.status?.active, flag: false) : ''; // меню трея рисует система: флаги-эмодзи там не видны
    final sig = '$ts|${v?.mode}|${model.phase}|$brief';
    if (sig == _sig) return;
    _sig = sig;
    final ready = model.phase == Phase.ready && v != null;
    await trayManager.setIcon(trayIconPath(ts));
    await _windowIcon(trayIcons[ts]!);
    await _tip(!ready ? 'Brod' : switch (ts) {
      TrayState.connecting => 'Подключаюсь…',
      TrayState.problem => 'VPN включён, но выход не отвечает',
      TrayState.on => 'VPN включён · ${brief.isEmpty ? (vpnModes[v.mode] ?? v.mode) : brief}',
      TrayState.off => 'VPN выключен',
    });
    await trayManager.setContextMenu(Menu(items: [
      if (brief.isNotEmpty) ...[MenuItem(key: 'brief', label: brief, disabled: true), MenuItem.separator()],
      MenuItem(key: 'open', label: 'Открыть окно'),
      MenuItem.separator(),
      MenuItem(key: 'best', label: 'Подключиться к лучшему узлу', disabled: !ready || busy),
      MenuItem(key: 'toggle', label: busy ? 'Переключаю…' : (on ? 'Выключить VPN' : 'Включить VPN'), disabled: !ready || busy),
      MenuItem.submenu(label: 'Режим', submenu: Menu(items: [
        for (final e in vpnModes.entries) MenuItem.checkbox(key: 'mode:${e.key}', label: e.value, checked: v?.mode == e.key, disabled: !ready || busy),
      ])),
      MenuItem.separator(),
      MenuItem(key: 'quit', label: 'Выйти'),
    ]));
  }

  // ---- трей ----
  @override
  void onTrayIconMouseDown() => showWindow();

  @override
  void onTrayIconRightMouseDown() => trayManager.popUpContextMenu();

  @override
  void onTrayMenuItemClick(MenuItem menuItem) {
    final k = menuItem.key ?? '';
    if (k == 'open') {
      showWindow();
    } else if (k == 'best') {
      model.connectBest();
    } else if (k == 'toggle') {
      model.setOn(!(model.vpn?.on ?? false));
    } else if (k.startsWith('mode:')) {
      model.setMode(k.substring(5));
    } else if (k == 'quit') {
      quit();
    }
  }

  // ---- окно ----
  @override
  void onWindowMinimize() => model.setForeground(false);

  @override
  void onWindowRestore() => model.setForeground(true);

  @override
  void onWindowFocus() => model.setForeground(true);

  @override
  void onWindowClose() {
    if (prefs.closeToTray) {
      windowManager.hide();
      model.setForeground(false); // окно спрятано: страницы и монитор не опрашиваются, главный опрос реже (как на Android в фоне)
    } else {
      quit();
    }
  }
}
