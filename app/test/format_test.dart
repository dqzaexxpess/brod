import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/format.dart';

void main() {
  test('форматирование размеров и скоростей', () {
    expect(fmtBytes(0), '0 Б');
    expect(fmtBytes(1023), '1023 Б');
    expect(fmtBytes(1536), '1,5 КБ');
    expect(fmtBytes(5 * 1024 * 1024), '5,0 МБ');
    expect(fmtBytes(250 * 1024 * 1024), '250 МБ');
    expect(fmtRate(2048), '2,0 КБ/с');
  });
}
