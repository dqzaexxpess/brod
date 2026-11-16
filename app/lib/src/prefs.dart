import 'dart:convert';
import 'dart:io';
import 'dart:ui';

import 'package:flutter/foundation.dart';
import 'platform_paths.dart';
import 'window_geometry.dart';

/// Акценты оформления: название и основной цвет.
const accentPalette = <(String, int)>[
  ('Бирюзовый', 0xFF2E8B7A),
  ('Синий', 0xFF3F7DE0),
  ('Фиолетовый', 0xFF7C5CE0),
  ('Оранжевый', 0xFFE0803F),
  ('Розовый', 0xFFD9527A),
  ('Графитовый', 0xFF607080),
];

/// Настройки самого приложения (окно, трей, оформление): хранятся рядом с остальными конфигами пользователя, не в демоне.
class Prefs extends ChangeNotifier {
  Prefs({this.closeToTray = true, this.startHidden = false, this.themeMode = 'system', this.accent = 0, this.amoled = false, this.compact = false, this.groupByCountry = true, this.file});

  bool closeToTray; // крестик сворачивает в трей (иначе — выход)
  bool startHidden; // запускаться свёрнутым в трей
  String themeMode; // system | light | dark
  int accent; // индекс в accentPalette
  bool amoled; // чисто чёрный фон в тёмной теме
  bool compact; // плотный список узлов
  bool groupByCountry; // группировать список узлов по странам
  Set<String> collapsed = {}; // свёрнутые группы списка узлов (код страны, '#own', '#none')
  Rect? windowBounds; // положение и размер окна, чтобы открываться там же, где закрыли
  final File? file;

  Color get accentColor => Color(accentPalette[accent.clamp(0, accentPalette.length - 1)].$2);

  static File defaultFile() {
    final f = File(appConfigFilePath(Platform.environment, windows: Platform.isWindows));
    if (!f.existsSync()) {
      // переименование в Brod: настройки из прежнего каталога vpn-client переносятся при первом запуске (прежний файл остаётся)
      try {
        final old = File(legacyAppConfigFilePath(Platform.environment, windows: Platform.isWindows));
        if (old.existsSync()) {
          f.parent.createSync(recursive: true);
          old.copySync(f.path);
        }
      } catch (_) {}
    }
    return f;
  }

  static Prefs load([File? file]) {
    final f = file ?? defaultFile();
    try {
      final j = jsonDecode(f.readAsStringSync()) as Map<String, dynamic>;
      final tm = j['theme_mode'];
      return Prefs(
        closeToTray: j['close_to_tray'] != false,
        startHidden: j['start_hidden'] == true,
        themeMode: tm == 'light' || tm == 'dark' ? tm as String : 'system',
        accent: ((j['accent'] as num?)?.toInt() ?? 0).clamp(0, accentPalette.length - 1),
        amoled: j['amoled'] == true,
        compact: j['compact'] == true,
        groupByCountry: j['group_by_country'] != false,
        file: f,
      )
        ..collapsed = {for (final k in (j['collapsed_groups'] as List?) ?? const []) k.toString()}
        ..windowBounds = boundsFromJson(j['window']);
    } catch (_) {
      return Prefs(file: f);
    }
  }

  void update({bool? closeToTray, bool? startHidden, String? themeMode, int? accent, bool? amoled, bool? compact, bool? groupByCountry}) {
    this.closeToTray = closeToTray ?? this.closeToTray;
    this.startHidden = startHidden ?? this.startHidden;
    this.themeMode = themeMode ?? this.themeMode;
    this.accent = accent ?? this.accent;
    this.amoled = amoled ?? this.amoled;
    this.compact = compact ?? this.compact;
    this.groupByCountry = groupByCountry ?? this.groupByCountry;
    _save();
    notifyListeners();
  }

  /// Запоминает положение и размер окна (слушателей не будят: интерфейс от этого не меняется).
  void setWindowBounds(Rect r) {
    if (windowBounds == r) return;
    windowBounds = r;
    _save();
  }

  /// Свернуть/развернуть одну группу списка узлов.
  void toggleGroup(String key) => setCollapsed(collapsed.contains(key) ? (collapsed.toSet()..remove(key)) : (collapsed.toSet()..add(key)));

  void setCollapsed(Set<String> keys) {
    collapsed = keys;
    _save();
    notifyListeners();
  }

  void _save() {
    final f = file;
    if (f == null) return;
    try {
      f.parent.createSync(recursive: true);
      f.writeAsStringSync(jsonEncode({'close_to_tray': closeToTray, 'start_hidden': startHidden, 'theme_mode': themeMode, 'accent': accent, 'amoled': amoled, 'compact': compact, 'group_by_country': groupByCountry, 'collapsed_groups': collapsed.toList()..sort(), if (windowBounds != null) 'window': boundsToJson(windowBounds!)}));
    } catch (_) {}
  }
}
