import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/daemon_errors.dart';

void main() {
  test('«демона нет» отличается от прочих ошибок', () {
    for (final code in [2, 111, 10061]) {
      expect(daemonNotRunning(SocketException('x', osError: OSError('', code))), isTrue, reason: 'код $code');
    }
    expect(daemonNotRunning(SocketException('x', osError: OSError('', 13))), isFalse);
    expect(daemonNotRunning(StateError('x')), isFalse);
  });

  test('текст ошибки: без сырого SocketException, с хвостом журнала', () {
    final e = SocketException('Connection failed', osError: OSError('отказ', 10061));
    expect(daemonDownText(e), 'Демон не запущен.');
    final t = daemonDownText(e, logTail: 'a\nb');
    expect(t, contains('Демон не запущен.'));
    expect(t, contains('brodd.log'));
    expect(t, isNot(contains('SocketException')));
    expect(daemonDownText(StateError('boom')), contains('Демон не отвечает'));
  });

  test('хвост журнала', () {
    expect(tailLines('1\n\n2\n3\n4\n', 2), '3\n4');
    expect(tailLines('', 3), '');
  });

  test('отказ в доступе: не «демон не запущен», а понятная причина', () {
    for (final code in [13, 5, 10013]) {
      final e = SocketException('x', osError: OSError('', code));
      expect(daemonAccessDenied(e), isTrue, reason: 'код $code');
      expect(daemonNotRunning(e), isFalse);
    }
    final e = SocketException('x', osError: OSError('', 13));
    expect(daemonDownText(e, windows: true), contains('Переустановите'));
    expect(daemonDownText(e, windows: false), contains('права'));
    expect(daemonAccessDenied(StateError('x')), isFalse);
    expect(daemonAccessDenied(SocketException('x', osError: OSError('', 111))), isFalse);
  });
}
