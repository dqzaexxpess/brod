import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/prefs.dart';

void main() {
  welcomeTest();
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

void welcomeTest() {
  test('мастер первого запуска: флаг переживает перезапуск', () {
    final dir = Directory.systemTemp.createTempSync('prefs');
    final f = File('${dir.path}/app.json');
    final p = Prefs.load(f);
    expect(p.welcomeDone, false);
    p.update(welcomeDone: true);
    expect(Prefs.load(f).welcomeDone, true);
    dir.deleteSync(recursive: true);
  });

  test('свой цвет и размер текста сохраняются; палитра расширена без сдвига прежних индексов', () {
    expect(accentPalette[3].$2, 0xFFE0803F, reason: 'прежние индексы не меняются: сохранённый выбор остаётся тем же');
    expect(accentPalette.length, greaterThan(6));
    final dir = Directory.systemTemp.createTempSync('brod-prefs');
    addTearDown(() => dir.deleteSync(recursive: true));
    final f = File('${dir.path}/app.json');
    final p = Prefs.load(f);
    expect(p.textScale, 1.0);
    p.update(accent: -1, customAccent: 0xFF3F7DE0, textScale: 1.15);
    final q = Prefs.load(f);
    expect(q.accent, -1);
    expect(q.accentColor.toARGB32(), 0xFF3F7DE0);
    expect(q.textScale, 1.15);
    // повреждённые значения не ломают загрузку: размер текста в пределах, свой цвет без значения — обычный акцент
    f.writeAsStringSync('{"accent":-1,"text_scale":9}');
    final r = Prefs.load(f);
    expect(r.textScale, 1.4);
    expect(r.accent, 0);
  });

  test('HEX-цвет разбирается с решёткой и без, мусор отвергается', () {
    expect(parseHexColor('#3F7DE0')?.toARGB32(), 0xFF3F7DE0);
    expect(parseHexColor(' 3f7de0 ')?.toARGB32(), 0xFF3F7DE0);
    for (final bad in ['', '#12345', '#1234567', 'gggggg', '#3F7DE0FF']) {
      expect(parseHexColor(bad), isNull, reason: bad);
    }
  });
}
