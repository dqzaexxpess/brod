import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api_access.dart';

void main() {
  test('файл доступа к демону по HTTP', () {
    final a = parseApiAccess('{"addr":"127.0.0.1:29081","token":"abc"}')!;
    expect(a.base, 'http://127.0.0.1:29081');
    expect(a.token, 'abc');
    expect(parseApiAccess('не json'), isNull);
    expect(parseApiAccess('{"addr":"127.0.0.1:1"}'), isNull);
    expect(parseApiAccess('{"addr":"","token":"x"}'), isNull);
    expect(parseApiAccess('[]'), isNull);
  });

  test('отказ в доступе к сокету отличается от «демона нет»', () {
    expect(socketAccessDenied(5, windows: true), isTrue);
    expect(socketAccessDenied(10013, windows: true), isTrue);
    expect(socketAccessDenied(10061, windows: true), isFalse, reason: 'отказ в соединении — демон не слушает');
    expect(socketAccessDenied(2, windows: true), isFalse, reason: 'нет файла сокета');
    expect(socketAccessDenied(13, windows: false), isFalse, reason: 'на Linux запасного пути нет');
    expect(socketAccessDenied(null, windows: true), isFalse);
  });
}
