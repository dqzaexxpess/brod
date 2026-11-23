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

  testWidgets('поиск: находит настройку по синониму и ведёт в её раздел', (tester) async {
    final m = await _pump(tester, const Size(420, 900));
    await tester.enterText(find.byType(SearchBar), 'телевизор');
    await tester.pumpAndSettle();
    expect(find.text('Раздавать прокси в локальную сеть'), findsOneWidget);
    expect(find.text('Подключение'), findsNothing, reason: 'вместо списка разделов — результаты');
    await tester.tap(find.text('Раздавать прокси в локальную сеть'));
    await tester.pump(const Duration(milliseconds: 900));
    expect(find.text('Раздавать прокси в локальную сеть'), findsOneWidget, reason: 'открылась «Маршрутизация», настройка на экране');
    await tester.pump(const Duration(seconds: 4)); // подсветка гаснет
    await tester.pageBack();
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(SearchBar), 'абракадабра');
    await tester.pumpAndSettle();
    expect(find.textContaining('Ничего не найдено'), findsOneWidget);
    await tester.tap(find.byTooltip('Очистить'));
    await tester.pumpAndSettle();
    expect(find.text('Подключение'), findsOneWidget, reason: 'после очистки снова список разделов');
    m.dispose();
  });

  testWidgets('поиск на широком окне: выбор результата открывает раздел справа и сбрасывает запрос', (tester) async {
    final m = await _pump(tester, const Size(1200, 800));
    await tester.enterText(find.byType(SearchBar), 'киллсвитч');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Киллсвитч').first);
    await tester.pump(const Duration(milliseconds: 900));
    await tester.pump(const Duration(seconds: 4));
    expect(find.text('Защита от утечек'), findsOneWidget);
    expect(find.text('Подключение'), findsOneWidget, reason: 'список разделов вернулся');
    m.dispose();
  });

  testWidgets('поиск: после ухода в раздел и возврата поле не берёт фокус и не поднимает клавиатуру', (tester) async {
    final m = await _pump(tester, const Size(420, 900));
    await tester.tap(find.byType(SearchBar));
    await tester.pump();
    expect(tester.testTextInput.isVisible, true, reason: 'поле поиска в фокусе, клавиатура показана');
    await tester.tap(find.text('Подключение')); // жест «закрыть» клавиатуру фокус не снимает; выбор раздела — должен
    await tester.pumpAndSettle();
    await tester.pageBack();
    await tester.pumpAndSettle();
    expect(tester.testTextInput.isVisible, false, reason: 'после возврата клавиатуры быть не должно');
    m.dispose();
  });

  testWidgets('поиск: результат открывает раздел, прокручивает к настройке и подсвечивает её', (tester) async {
    final m = await _pump(tester, const Size(420, 900));
    await tester.enterText(find.byType(SearchBar), 'fakeip');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Быстрый DNS (FakeIP)'));
    await tester.pump(const Duration(milliseconds: 800)); // переход + прокрутка
    await tester.pump(const Duration(milliseconds: 400));
    final target = find.text('Быстрый DNS (FakeIP), эксперимент');
    expect(target, findsOneWidget, reason: 'открылся нужный раздел');
    final lit = find.ancestor(
      of: target,
      matching: find.byWidgetPredicate((w) => w is Material && w.color != null && (w.color!.a - 0.7).abs() < 0.01),
    );
    expect(lit, findsOneWidget, reason: 'настройка подсвечена');
    await tester.pump(const Duration(seconds: 3)); // подсветка гаснет
    await tester.pumpAndSettle();
    m.dispose();
  });

  testWidgets('поиск: функция из меню узла (QR) ведёт на вкладку «Узлы» и подсказывает, куда нажать', (tester) async {
    await tester.binding.setSurfaceSize(const Size(420, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final m = AppModel(_Api(), pollEvery: const Duration(hours: 1));
    var shown = 0;
    await tester.pumpWidget(MaterialApp(home: Scaffold(body: SettingsPage(model: m, onShowNodes: () => shown++))));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(SearchBar), 'qr');
    await tester.pumpAndSettle();
    await tester.tap(find.text('QR-код сервера'));
    await tester.pumpAndSettle();
    expect(shown, 1, reason: 'переключились на вкладку «Узлы»');
    expect(m.toast, contains('Показать QR-код'), reason: 'подсказка, где находится действие');
    m.dispose();
  });
}
