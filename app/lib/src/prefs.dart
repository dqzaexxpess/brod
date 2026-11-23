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
  // добавлены позже: индексы прежних цветов не меняются, чтобы сохранённый выбор остался
  ('Красный', 0xFFD64545),
  ('Зелёный', 0xFF3E9B4F),
  ('Салатовый', 0xFF8DB63C),
  ('Янтарный', 0xFFE0A33F),
  ('Голубой', 0xFF29A3C7),
  ('Индиго', 0xFF4A55B8),
  ('Бордовый', 0xFF9B2D4E),
  ('Мятный', 0xFF4CC3A0),
];

/// Допустимые размеры текста интерфейса (множитель к системному).
const textScales = <(String, double)>[('Мельче', 0.9), ('Обычный', 1.0), ('Крупнее', 1.15), ('Ещё крупнее', 1.3)];

/// Цвет из HEX-строки «#RRGGBB» или «RRGGBB» (без прозрачности); null — не разобралась.
Color? parseHexColor(String text) {
  final t = text.trim().replaceFirst('#', '');
  if (!RegExp(r'^[0-9a-fA-F]{6}$').hasMatch(t)) return null;
  return Color(0xFF000000 | int.parse(t, radix: 16));
}

/// Настройки самого приложения (окно, трей, оформление): хранятся рядом с остальными конфигами пользователя, не в демоне.
class Prefs extends ChangeNotifier {
  Prefs({this.closeToTray = true, this.startHidden = false, this.themeMode = 'system', this.accent = 0, this.customAccent, this.textScale = 1.0, this.amoled = false, this.compact = false, this.groupByCountry = true, this.welcomeDone = false, this.clipboardImport = true, this.file});

  bool closeToTray; // крестик сворачивает в трей (иначе — выход)
  bool startHidden; // запускаться свёрнутым в трей
  String themeMode; // system | light | dark
  int accent; // индекс в accentPalette; -1 — свой цвет (customAccent)
  int? customAccent; // свой акцентный цвет, ARGB
  double textScale; // множитель размера текста
  bool amoled; // чисто чёрный фон в тёмной теме
  bool compact; // плотный список узлов
  bool groupByCountry; // группировать список узлов по странам
  bool welcomeDone; // мастер первого запуска пройден или пропущен
  bool clipboardImport; // Android: при возврате в приложение предлагать добавить ссылку на узел из буфера обмена
  Set<String> collapsed = {}; // свёрнутые группы списка узлов (код страны, '#own', '#none')
  Rect? windowBounds; // положение и размер окна, чтобы открываться там же, где закрыли
  final File? file;

  Color get accentColor => accent < 0 && customAccent != null ? Color(customAccent!) : Color(accentPalette[accent.clamp(0, accentPalette.length - 1)].$2);

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
        accent: ((j['accent'] as num?)?.toInt() ?? 0) < 0 && j['custom_accent'] is num ? -1 : ((j['accent'] as num?)?.toInt() ?? 0).clamp(0, accentPalette.length - 1),
        customAccent: (j['custom_accent'] as num?)?.toInt(),
        textScale: ((j['text_scale'] as num?)?.toDouble() ?? 1.0).clamp(0.8, 1.4).toDouble(),
        amoled: j['amoled'] == true,
        compact: j['compact'] == true,
        groupByCountry: j['group_by_country'] != false,
        welcomeDone: j['welcome_done'] == true,
        clipboardImport: j['clipboard_import'] != false,
        file: f,
      )
        ..collapsed = {for (final k in (j['collapsed_groups'] as List?) ?? const []) k.toString()}
        ..windowBounds = boundsFromJson(j['window']);
    } catch (_) {
      return Prefs(file: f);
    }
  }

  void update({bool? closeToTray, bool? startHidden, String? themeMode, int? accent, int? customAccent, double? textScale, bool? amoled, bool? compact, bool? groupByCountry, bool? welcomeDone, bool? clipboardImport}) {
    this.closeToTray = closeToTray ?? this.closeToTray;
    this.startHidden = startHidden ?? this.startHidden;
    this.themeMode = themeMode ?? this.themeMode;
    this.accent = accent ?? this.accent;
    this.customAccent = customAccent ?? this.customAccent;
    this.textScale = textScale ?? this.textScale;
    this.amoled = amoled ?? this.amoled;
    this.compact = compact ?? this.compact;
    this.groupByCountry = groupByCountry ?? this.groupByCountry;
    this.welcomeDone = welcomeDone ?? this.welcomeDone;
    this.clipboardImport = clipboardImport ?? this.clipboardImport;
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
      f.writeAsStringSync(jsonEncode({'close_to_tray': closeToTray, 'start_hidden': startHidden, 'theme_mode': themeMode, 'accent': accent, if (customAccent != null) 'custom_accent': customAccent, 'text_scale': textScale, 'amoled': amoled, 'compact': compact, 'group_by_country': groupByCountry, 'welcome_done': welcomeDone, 'clipboard_import': clipboardImport, 'collapsed_groups': collapsed.toList()..sort(), if (windowBounds != null) 'window': boundsToJson(windowBounds!)}));
    } catch (_) {}
  }
}
