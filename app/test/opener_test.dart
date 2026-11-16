// T-9: openLink (раньше покрытие 0%): команда и аргументы, сообщения об ошибках. Внешняя команда подменена: ничего не запускается.
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/opener.dart';

void main() {
  final platform = Platform.isLinux ? ('xdg-open', <String>[]) : Platform.isWindows ? ('rundll32', ['url.dll,FileProtocolHandler']) : Platform.isMacOS ? ('open', <String>[]) : null;

  test('ссылка целиком уходит системному обработчику, успех — null', () async {
    final calls = <(String, List<String>)>[];
    const url = 'tg://proxy?server=a.example&port=443&secret=ff';
    final r = await openLink(url, run: (cmd, args) async {
      calls.add((cmd, args));
      return ProcessResult(1, 0, '', '');
    });
    if (platform == null) {
      expect(r, contains('не реализовано'));
      return;
    }
    expect(r, isNull);
    expect(calls.single.$1, platform.$1);
    expect(calls.single.$2, [...platform.$2, url], reason: 'адрес с & передаётся одним аргументом, без разбора оболочкой');
  });

  test('ненулевой код выхода — сообщение пользователю', () async {
    final r = await openLink('https://example.org', run: (_, _) async => ProcessResult(1, 3, '', 'нет обработчика'));
    expect(r, platform == null ? contains('не реализовано') : contains('Не удалось открыть ссылку'));
  });

  test('команды нет в системе (ProcessException) — сообщение с причиной, не исключение', () async {
    final r = await openLink('https://example.org', run: (cmd, _) async => throw ProcessException(cmd, const [], 'нет такой команды'));
    expect(r, platform == null ? contains('не реализовано') : allOf(contains('Не удалось открыть ссылку'), contains('нет такой команды')));
  });
}
