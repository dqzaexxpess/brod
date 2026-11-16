import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/extras.dart';
import 'package:brod/src/model.dart';

// сам диалог подтверждения восстановления из копии (логика сводки покрыта в app_test.dart).
void main() {
  final summary = AppModel.backupSummary({
    'settings': {'killswitch': '0'},
    'rules': [1, 2],
    'sources': [
      {'name': 'чужой', 'url': 'https://example.org/sub'}
    ],
  });

  Future<bool?> open(WidgetTester tester, void Function(Future<bool> r) onResult) async {
    await tester.pumpWidget(MaterialApp(
      home: Builder(
        builder: (context) => Scaffold(body: TextButton(onPressed: () => onResult(confirmRestore(context, summary)), child: const Text('открыть'))),
      ),
    ));
    await tester.tap(find.text('открыть'));
    await tester.pumpAndSettle();
    return null;
  }

  testWidgets('показывает заголовок, сводку, предупреждение про чужие копии и обе кнопки', (tester) async {
    await open(tester, (_) {});
    expect(find.text('Копия изменит'), findsOneWidget);
    expect(find.textContaining('killswitch = 0'), findsOneWidget);
    expect(find.textContaining('https://example.org/sub'), findsOneWidget);
    expect(find.textContaining('Правила маршрутизации: заменятся, будет 2'), findsOneWidget);
    expect(find.textContaining('Восстанавливайте только свою копию'), findsOneWidget);
    expect(find.widgetWithText(TextButton, 'Отмена'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, 'Применить'), findsOneWidget);
  });

  testWidgets('«Отмена» возвращает false, окно закрывается', (tester) async {
    late Future<bool> r;
    await open(tester, (f) => r = f);
    await tester.tap(find.text('Отмена'));
    await tester.pumpAndSettle();
    expect(await r, isFalse);
    expect(find.text('Копия изменит'), findsNothing);
  });

  testWidgets('«Применить» возвращает true, окно закрывается', (tester) async {
    late Future<bool> r;
    await open(tester, (f) => r = f);
    await tester.tap(find.text('Применить'));
    await tester.pumpAndSettle();
    expect(await r, isTrue);
    expect(find.text('Копия изменит'), findsNothing);
  });
}
