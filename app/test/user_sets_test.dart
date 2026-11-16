import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/user_sets_page.dart';

import 'app_test.dart' show FakeApi;

void main() {
  rulesMatchTests();
  testWidgets('свои списки: добавление по адресу и удаление', (tester) async {
    await tester.binding.setSurfaceSize(const Size(500, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = FakeApi();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(MaterialApp(home: UserSetsPage(model: m)));
    await tester.pumpAndSettle();
    expect(find.textContaining('Своих списков нет'), findsOneWidget);
    await tester.tap(find.text('Добавить список'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Список не подписан'), findsOneWidget, reason: 'предупреждение о доверии');
    await tester.enterText(find.byType(TextField).first, 'https://example.org/blocked.srs');
    await tester.enterText(find.byType(TextField).last, 'Мой список');
    await tester.tap(find.text('Добавить'));
    await tester.pumpAndSettle();
    expect(api.calls, contains('add set https://example.org/blocked.srs'));
    expect(find.text('Мой список'), findsOneWidget);
    await tester.tap(find.byTooltip('Удалить'));
    await tester.pumpAndSettle();
    expect(api.calls, contains('remove set ext-0a1b2c3d'));
    expect(find.textContaining('Своих списков нет'), findsOneWidget);
    m.dispose();
  });
}

void rulesMatchTests() {
  test('условия правила, которых экран не знает, не теряются при правке', () {
    final m = RouteMatch.fromJson({
      'domain_suffix': ['example.org'],
      'wifi_ssid': ['Home'],
      'future_condition': ['x'],
      'invert': true,
    });
    expect(m.wifiSsid, ['Home']);
    expect(m.extra.keys, ['future_condition']);
    final back = m.copy().toJson();
    expect(back['future_condition'], ['x']);
    expect(back['wifi_ssid'], ['Home']);
    expect(back['invert'], true);
    expect(m.isEmpty, isFalse, reason: 'правило только с неизвестным условием не считается пустым');
    expect(RouteMatch.fromJson({'future_condition': 1}).isEmpty, isFalse);
  });
}
