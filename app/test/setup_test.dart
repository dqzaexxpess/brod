import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/setup/setup_app.dart';
import 'package:brod/src/setup/setup_args.dart';
import 'package:brod/src/setup/setup_runner.dart';

class FakeRunner implements SetupRunner {
  FakeRunner(this.code, [this.lines = const ['Копирование файлов']]);

  final int code;
  final List<String> lines;
  String? account;
  bool? removeData;
  int calls = 0;

  @override
  Future<SetupOutcome> install({required String account, required String dir, required void Function(String) onLine}) async {
    this.account = account;
    calls++;
    lines.forEach(onLine);
    return SetupOutcome(code, lines);
  }

  @override
  Future<SetupOutcome> uninstall({required String dir, required bool removeData, required void Function(String) onLine}) async {
    this.removeData = removeData;
    calls++;
    return SetupOutcome(code, lines);
  }
}

Widget wizard(SetupMode mode, FakeRunner r, {List<int>? exits, List<String>? launched}) => MaterialApp(
      theme: setupTheme(),
      home: SetupWizard(
        mode: mode,
        runner: r,
        installDir: r'C:\Program Files\Brod',
        account: r'PC\misav',
        onExit: (c) => exits?.add(c),
        launchApp: (d) async => launched?.add(d),
      ),
    );

void main() {
  test('аргументы запуска', () {
    expect(parseSetupArgs(['--setup']).mode, SetupMode.install);
    final u = parseSetupArgs(['--uninstall', '--from-temp', '--install-dir', r'C:\Program Files\Brod']);
    expect(u.mode, SetupMode.uninstall);
    expect(u.fromTemp, isTrue);
    expect(u.installDir, r'C:\Program Files\Brod');
    expect(parseSetupArgs(const []).isSetup, isFalse);
  });

  test('учётная запись по умолчанию — домен и имя', () {
    expect(defaultAccount({'USERDOMAIN': 'PC', 'USERNAME': 'misav'}), r'PC\misav');
    expect(defaultAccount({'USERNAME': 'misav'}), 'misav');
    expect(defaultAccount({}), '');
  });

  test('протокол PowerShell: заголовок на любом языке и шум ошибок отброшены, повторы схлопнуты', () {
    const raw = '**********************\nНачало сеанса Windows PowerShell\nВремя начала: 20261117\nИмя пользователя: PC\\misav\n**********************\n'
        'Копирование файлов\n'
        "УСТАНОВКА НЕ ЗАВЕРШЕНА: учётная запись 'НЕТ\\ТАКОГО' не найдена.\n"
        "УСТАНОВКА НЕ ЗАВЕРШЕНА: учётная запись 'НЕТ\\ТАКОГО' не найдена.\n"
        'PS>TerminatingError(): "x"\n'
        r'C:\Temp\install.ps1:76 знак:13' '\n'
        '+ catch { throw "..." }\n'
        '**********************\nКонец сеанса\n**********************\n';
    final lines = transcriptLines(raw);
    expect(lines, ['Копирование файлов', "УСТАНОВКА НЕ ЗАВЕРШЕНА: учётная запись 'НЕТ\\ТАКОГО' не найдена."]);
    expect(failureSummary(lines), "Учётная запись 'НЕТ\\ТАКОГО' не найдена.");
    expect(failureSummary(const ['Копирование']), 'Копирование');
    expect(failureSummary(const []), '');
    expect(decodeLog([0xEF, 0xBB, 0xBF, 0xD0, 0x93]), 'Г');
    expect(decodeLog([0xFF, 0xFE, 0x13, 0x04]), 'Г');
  });

  test('сценарий-запускающий: права администратора, код отказа, кавычки в путях', () {
    final s = WindowsSetupRunner.launcher(r"C:\Temp\it's\install.ps1", [r'-User', r'PC\misav'], r'C:\Temp\log.txt');
    expect(s, contains('-Verb RunAs'));
    expect(s, contains('exit 1223'));
    expect(s, contains("it''s"));
    expect(s, contains('-Log'));
  });

  testWidgets('установка: учётная запись уходит в скрипт, затем запуск Brod и выход', (t) async {
    final r = FakeRunner(0);
    final exits = <int>[];
    final launched = <String>[];
    await t.pumpWidget(wizard(SetupMode.install, r, exits: exits, launched: launched));
    expect(find.text('Установить'), findsOneWidget);
    await t.tap(find.text('Установить'));
    await t.pumpAndSettle();
    expect(r.account, r'PC\misav');
    expect(find.text('Brod установлен'), findsOneWidget);
    await t.tap(find.text('Готово'));
    await t.pumpAndSettle();
    expect(launched, [r'C:\Program Files\Brod']);
    expect(exits, [0]);
  });

  testWidgets('установка: без галочки Brod не запускается', (t) async {
    final r = FakeRunner(0);
    final launched = <String>[];
    await t.pumpWidget(wizard(SetupMode.install, r, launched: launched, exits: []));
    await t.tap(find.text('Установить'));
    await t.pumpAndSettle();
    await t.tap(find.text('Запустить Brod'));
    await t.tap(find.text('Готово'));
    await t.pumpAndSettle();
    expect(launched, isEmpty);
  });

  testWidgets('пустая учётная запись не отправляется', (t) async {
    final r = FakeRunner(0);
    await t.pumpWidget(MaterialApp(
      theme: setupTheme(),
      home: SetupWizard(mode: SetupMode.install, runner: r, installDir: 'x', account: '', onExit: (_) {}, launchApp: (_) async {}),
    ));
    await t.enterText(find.byType(TextField), '   ');
    await t.tap(find.text('Установить'));
    await t.pumpAndSettle();
    expect(r.calls, 0);
  });

  testWidgets('ошибка: показан протокол, «Повторить» запускает заново', (t) async {
    final r = FakeRunner(1, ['Копирование', 'не удалось определить учётную запись']);
    await t.pumpWidget(wizard(SetupMode.install, r, exits: []));
    await t.tap(find.text('Установить'));
    await t.pumpAndSettle();
    expect(find.text('Не удалось установить'), findsOneWidget);
    expect(find.textContaining('не удалось определить учётную запись'), findsWidgets);
    await t.tap(find.text('Назад'));
    await t.pumpAndSettle();
    expect(find.text('Установить'), findsOneWidget, reason: 'после ошибки можно вернуться к полю учётной записи');
    await t.tap(find.text('Установить'));
    await t.pumpAndSettle();
    await t.tap(find.text('Повторить'));
    await t.pumpAndSettle();
    expect(r.calls, 3);
  });

  testWidgets('отказ от повышения прав объясняется, а не выглядит ошибкой установки', (t) async {
    final r = FakeRunner(SetupOutcome.elevationDeclined, const []);
    await t.pumpWidget(wizard(SetupMode.install, r, exits: []));
    await t.tap(find.text('Установить'));
    await t.pumpAndSettle();
    expect(find.text('Нужны права администратора'), findsOneWidget);
  });

  testWidgets('удаление: по умолчанию данные сохраняются, галочка их удаляет', (t) async {
    final r = FakeRunner(0);
    await t.pumpWidget(wizard(SetupMode.uninstall, r, exits: []));
    await t.tap(find.text('Удалить'));
    await t.pumpAndSettle();
    expect(r.removeData, isFalse);
    expect(find.text('Brod удалён'), findsOneWidget);

    final r2 = FakeRunner(0);
    await t.pumpWidget(const SizedBox()); // сбросить состояние мастера
    await t.pumpWidget(wizard(SetupMode.uninstall, r2, exits: []));
    await t.tap(find.byType(CheckboxListTile));
    await t.tap(find.text('Удалить'));
    await t.pumpAndSettle();
    expect(r2.removeData, isTrue);
  });
}
