import 'dart:ui';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/window_geometry.dart';

void main() {
  const screen = Rect.fromLTWH(0, 0, 1920, 1040);
  const second = Rect.fromLTWH(1920, 0, 1280, 984);

  test('окно на экране возвращается как было', () {
    const r = Rect.fromLTWH(200, 100, 1000, 700);
    expect(restorableBounds(r, [screen]), r);
    expect(restorableBounds(const Rect.fromLTWH(2000, 50, 800, 600), [screen, second]), isNotNull);
  });

  test('монитор отключили: окно за краем экрана не возвращается', () {
    expect(restorableBounds(const Rect.fromLTWH(2000, 50, 800, 600), [screen]), isNull);
    expect(restorableBounds(const Rect.fromLTWH(1900, 1030, 800, 600), [screen]), isNull, reason: 'торчит углом — не видно');
  });

  test('слишком маленький или битый размер отбрасывается', () {
    expect(restorableBounds(const Rect.fromLTWH(0, 0, 100, 100), [screen]), isNull);
    expect(boundsFromJson({'x': 1, 'y': 2, 'w': 'a', 'h': 4}), isNull);
    expect(boundsFromJson(null), isNull);
  });

  test('запись и чтение', () {
    const r = Rect.fromLTWH(10.5, 20, 900, 600);
    expect(boundsFromJson(boundsToJson(r)), r);
  });
}
