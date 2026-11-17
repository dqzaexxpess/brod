import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/win_version.dart';

void main() {
  test('номер сборки разбирается из строки версии Dart', () {
    expect(windowsBuild('"Windows 10 Pro" 10.0 (Build 22000)'), 22000);
    expect(windowsBuild('Linux 6.14'), isNull);
  });

  test('предупреждение только для Windows без поддержки', () {
    expect(oldWindowsWarning('"Windows 10 Pro" 10.0 (Build 22000)'), contains('afunix'));
    expect(oldWindowsWarning('"Windows 10 Pro" 10.0 (Build 19044)'), isNotNull, reason: 'Windows 10 21H2');
    expect(oldWindowsWarning('"Windows 10 Pro" 10.0 (Build 19045)'), isNull, reason: 'Windows 10 22H2');
    expect(oldWindowsWarning('"Windows 11 Pro" 10.0 (Build 22621)'), isNull, reason: 'Windows 11 22H2');
    expect(oldWindowsWarning('"Windows 11 Pro" 10.0 (Build 26100)'), isNull);
    expect(oldWindowsWarning('Linux'), isNull);
  });
}
