import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/rules_page.dart';

/// Минимальный API для страницы правил: остальное не вызывается (noSuchMethod бросит, если вызовется).
class _RulesApi implements DaemonApi {
  final calls = <String>[];
  var profiles = const [RouteProfileDto(id: 'main', name: 'Основной', rules: 0, active: true)];

  @override
  Future<RouteRulesDto> routeRules() async => const RouteRulesDto([], ['geosite-youtube']);

  @override
  Future<List<RouteProfileDto>> routeProfiles() async => profiles;

  @override
  Future<RouteProfileDto> createRouteProfile(String name, {String from = ''}) async {
    calls.add('create $name from=$from');
    final p = RouteProfileDto(id: 'p1', name: name);
    profiles = [...profiles, p];
    return p;
  }

  @override
  Future<String?> activateRouteProfile(String id) async {
    calls.add('activate $id');
    profiles = [for (final p in profiles) RouteProfileDto(id: p.id, name: p.name, rules: p.rules, active: p.id == id)];
    return null;
  }

  @override
  Future<String> shareRouteProfile(String id) async => 'brod://route/x';

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  testWidgets('страница правил: меню профилей и добавление правил списком', (tester) async {
    await tester.binding.setSurfaceSize(const Size(900, 1400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _RulesApi();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(MaterialApp(home: RulesPage(model: m)));
    await tester.pumpAndSettle();
    expect(find.text('Основной'), findsOneWidget, reason: 'в заголовке имя активного профиля');

    // меню профилей
    await tester.tap(find.text('Основной'));
    await tester.pumpAndSettle();
    expect(find.text('Новый пустой профиль…'), findsOneWidget);
    expect(find.text('Копия активного профиля…'), findsOneWidget);
    await tester.tap(find.text('Новый пустой профиль…'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'Работа');
    await tester.tap(find.text('Готово'));
    await tester.pumpAndSettle();
    expect(api.calls, ['create Работа from=']);
    expect(find.text('Работа'), findsOneWidget, reason: 'новый профиль появился в списке');
    // переключение на него
    await tester.tap(find.text('Работа'));
    await tester.pumpAndSettle();
    expect(api.calls.last, 'activate p1');
    expect(m.activeProfile?.name, 'Работа');
  });

  testWidgets('«Добавить списком» создаёт по правилу на каждый вид и сообщает о неразобранном', (tester) async {
    await tester.binding.setSurfaceSize(const Size(900, 1400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _RulesApi();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(MaterialApp(home: RulesPage(model: m)));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Добавить списком'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Напрямую'));
    await tester.enterText(find.byType(TextField), 'example.com\n203.0.113.0/24\nпусть-не-разберётся\nprocess:firefox');
    await tester.tap(find.text('Добавить'));
    await tester.pumpAndSettle();
    expect(m.rules.length, 3, reason: 'сайты, IP и процессы — три отдельных правила');
    expect(m.rules.every((r) => r.action == 'direct'), isTrue);
    expect(m.rules.map((r) => r.title).toSet(), {'Список: Сайты', 'Список: IP-адреса', 'Список: Процессы'});
    expect(m.rulesDirty, isTrue);

    // пустой ввод не закрывает диалог и объясняет
    await tester.tap(find.byTooltip('Добавить списком'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Добавить'));
    await tester.pumpAndSettle();
    expect(find.text('Список пуст'), findsOneWidget);
  });
}
