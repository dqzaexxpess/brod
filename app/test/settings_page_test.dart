import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/settings_page.dart';

class _Api implements DaemonApi {
  final puts = <String>[];

  @override
  Future<List<SourceDto>> sources() async => const [];

  @override
  Future<Map<String, String>> settings() async => {'update.check': '1', 'killswitch.enabled': '1'};

  @override
  Future<void> putSetting(String key, String value) async => puts.add('$key=$value');

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

Future<AppModel> _pump(WidgetTester tester, Size size) async {
  await tester.binding.setSurfaceSize(size);
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final api = _Api();
  final m = AppModel(api, pollEvery: const Duration(hours: 1));
  await tester.pumpWidget(MaterialApp(home: Scaffold(body: SettingsPage(model: m))));
  await tester.pumpAndSettle();
  return m;
}

void main() {
  testWidgets('узкий экран: список разделов, содержимое открывается отдельным экраном', (tester) async {
    final m = await _pump(tester, const Size(420, 900));
    for (final t in ['Подключение', 'Маршрутизация', 'Узлы и источники', 'Уведомления', 'Оформление', 'Данные и диагностика', 'О приложении и обновление']) {
      expect(find.text(t), findsOneWidget, reason: t);
    }
    expect(find.text('Правила маршрутизации'), findsNothing, reason: 'пункты внутри разделов, не в общем списке');
    await tester.tap(find.text('Маршрутизация'));
    await tester.pumpAndSettle();
    expect(find.text('Правила маршрутизации'), findsOneWidget);
    expect(find.text('Обновление приложения'), findsNothing, reason: 'обновление не в маршрутизации');
    await tester.pageBack();
    await tester.pumpAndSettle();
    await tester.tap(find.text('О приложении и обновление'));
    await tester.pumpAndSettle();
    expect(find.text('Проверка и установка'), findsOneWidget);
    expect(find.text('Проверять обновления раз в сутки'), findsOneWidget);
    m.dispose();
  });

  testWidgets('широкое окно: список слева, раздел справа; переключатель пишет настройку', (tester) async {
    final m = await _pump(tester, const Size(1200, 800));
    expect(find.text('Защита от утечек'), findsOneWidget, reason: 'первый раздел открыт сразу');
    await tester.tap(find.text('О приложении и обновление'));
    await tester.pumpAndSettle();
    expect(find.text('Проверка и установка'), findsOneWidget);
    expect(find.text('Защита от утечек'), findsNothing);
    await tester.tap(find.text('Проверять обновления раз в сутки'));
    await tester.pumpAndSettle();
    expect((m.api as _Api).puts, contains('update.check=0'));
    m.dispose();
  });
}
