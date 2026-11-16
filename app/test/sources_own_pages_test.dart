// smoke-тесты страниц «Источники подписок» и «Мои серверы»: открываются, показывают список и пустое состояние,
// действия доходят до API.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/own_page.dart';
import 'package:brod/src/sources_page.dart';

class _Api implements DaemonApi {
  _Api({this.sourcesList = const [], this.own = const []});

  List<SourceDto> sourcesList;
  List<NodeDto> own;
  final calls = <String>[];

  @override
  Future<List<SourceDto>> sources() async => sourcesList;

  @override
  Future<Map<String, String>> settings() async => {};

  @override
  Future<void> addSource(String name, String url) async => calls.add('add:$name|$url');

  @override
  Future<void> removeSource(String name) async => calls.add('remove:$name');

  @override
  Future<void> setSourceEnabled(String name, bool on) async => calls.add('enable:$name:$on');

  @override
  Future<List<NodeDto>> nodes({bool alive = false, bool favorites = false, int limit = 300, String source = ''}) async => source == 'own' ? own : [];

  @override
  Future<String> addOwn(String text) async {
    calls.add('addOwn:$text');
    return 'добавлено: 1';
  }

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

Future<void> _open(WidgetTester tester, Widget page) async {
  await tester.binding.setSurfaceSize(const Size(900, 1600));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(MaterialApp(home: page));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('источники: пустой список показывает «Источников нет»', (tester) async {
    final m = AppModel(_Api(), pollEvery: const Duration(hours: 1));
    await _open(tester, SourcesPage(model: m));
    expect(find.text('Источники подписок'), findsOneWidget);
    expect(find.text('Источников нет'), findsOneWidget);
  });

  testWidgets('источники: список, выключатель, удаление своего и добавление через окно', (tester) async {
    final api = _Api(sourcesList: [
      const SourceDto(name: 'alpha', host: 'a.example', enabled: true, userAdded: false, lastCount: 12),
      const SourceDto(name: 'mine', host: 'm.example', enabled: true, userAdded: true, lastError: 'таймаут'),
    ]);
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, SourcesPage(model: m));
    expect(find.textContaining('alpha'), findsOneWidget);
    expect(find.textContaining('12 ссылок'), findsOneWidget);
    expect(find.textContaining('ошибка: таймаут'), findsOneWidget);
    // удалить можно только добавленный пользователем
    expect(find.byIcon(Icons.delete_outline), findsOneWidget);
    await tester.tap(find.byIcon(Icons.delete_outline));
    await tester.pumpAndSettle();
    expect(api.calls, contains('remove:mine'));
    await tester.tap(find.byType(Switch).at(1)); // [0] — «обновляется сам», [1] — alpha
    await tester.pumpAndSettle();
    expect(api.calls, contains('enable:alpha:false'));
    // добавление
    await tester.tap(find.text('Добавить источник'));
    await tester.pumpAndSettle();
    await tester.enterText(find.widgetWithText(TextField, 'Название (латиница, цифры, - _)'), 'new1');
    await tester.enterText(find.widgetWithText(TextField, 'Ссылка (https://…)'), 'https://x.example/sub');
    await tester.tap(find.text('Добавить'));
    await tester.pumpAndSettle();
    expect(api.calls, contains('add:new1|https://x.example/sub'));
  });

  testWidgets('мои серверы: пустое состояние и добавление ссылкой', (tester) async {
    final api = _Api();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, OwnPage(model: m));
    expect(find.text('Мои серверы'), findsOneWidget);
    expect(find.textContaining('Своих серверов пока нет'), findsOneWidget);
    await tester.tap(find.text('Добавить сервер'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'vless://x@vps.example:443');
    await tester.tap(find.text('Добавить'));
    await tester.pumpAndSettle();
    expect(api.calls, contains('addOwn:vless://x@vps.example:443'));
  });

  testWidgets('мои серверы: свой узел виден в списке', (tester) async {
    final api = _Api(own: [const NodeDto(id: 'o1', proto: 'vless', server: 'vps.example', port: 443, own: true)]);
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, OwnPage(model: m));
    expect(find.textContaining('vps.example'), findsWidgets);
    expect(find.textContaining('Своих серверов пока нет'), findsNothing);
  });
}
