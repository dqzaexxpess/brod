import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/prefs.dart';

void main() {
  test('настройки приложения сохраняются и читаются', () {
    final dir = Directory.systemTemp.createTempSync('prefs');
    final f = File('${dir.path}/sub/app.json');
    final p = Prefs.load(f);
    expect(p.closeToTray, true, reason: 'по умолчанию крестик сворачивает в трей');
    expect(p.startHidden, false);
    expect(p.themeMode, 'system');
    expect(p.accent, 0);
    expect(p.amoled, false);
    p.update(closeToTray: false, startHidden: true, themeMode: 'dark', accent: 3, amoled: true);
    final q = Prefs.load(f);
    expect(q.closeToTray, false);
    expect(q.startHidden, true);
    expect(q.themeMode, 'dark');
    expect(q.accent, 3);
    expect(q.amoled, true);
    expect(q.accentColor.toARGB32(), 0xFFE0803F);
    expect(q.collapsed, isEmpty);
    q.toggleGroup('NL');
    q.toggleGroup('#own');
    expect(Prefs.load(f).collapsed, {'NL', '#own'}, reason: 'свёрнутые группы переживают перезапуск');
    q.toggleGroup('NL');
    expect(Prefs.load(f).collapsed, {'#own'});
    q.setCollapsed({});
    expect(Prefs.load(f).collapsed, isEmpty);
    f.writeAsStringSync('мусор');
    expect(Prefs.load(f).closeToTray, true, reason: 'повреждённый файл — умолчания');
    dir.deleteSync(recursive: true);
  });
}
