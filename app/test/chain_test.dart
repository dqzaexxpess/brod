import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/node_dialogs.dart';

NodeDto node(String id, String proto, String server, {bool own = false, bool alive = false}) =>
    NodeDto(id: id, proto: proto, server: server, port: 443, own: own, l2At: alive ? DateTime(2026) : null);

class _Api implements DaemonApi {
  final created = <(String, List<String>)>[];

  @override
  Future<List<NodeDto>> nodes({bool alive = false, bool favorites = false, int limit = 300, String source = ''}) async => [
        node('own1', 'vless', 'vps.example.org', own: true),
        node('own2', 'trojan', 'vps2.example.org', own: true),
        node('free1', 'vmess', 'free.example.net', alive: true),
        node('wg1', 'wireguard', 'wg.example.net', alive: true),
        const NodeDto(id: 'chain1', proto: 'chain', server: '', port: 0, own: true, chain: [ChainHopDto(id: 'own1', proto: 'vless', server: 'vps.example.org', port: 443)]),
      ];

  @override
  Future<List<HopCheckDto>> checkChain(String id) async => const [
        HopCheckDto(server: 'a.example', port: 443, proto: 'vless', state: 'ok', ms: 40),
        HopCheckDto(server: 'b.example', port: 8443, proto: 'trojan', state: 'fail', detail: 'connection refused'),
        HopCheckDto(server: 'c.example', port: 443, proto: 'hysteria2', state: 'skip'),
      ];

  @override
  Future<String> addChain(String name, List<String> hops) async {
    created.add((name, hops));
    return 'new-chain';
  }

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  test('данные: звенья цепочки читаются из ответа демона, название — состав', () {
    final n = NodeDto.fromJson({
      'id': 'c1',
      'proto': 'chain',
      'class': 'chain/hops2/none',
      'server': '',
      'port': 0,
      'own': true,
      'chain': [
        {'id': 'a', 'proto': 'vless', 'server': 'first.example', 'port': 443},
        {'id': 'b', 'proto': 'trojan', 'server': 'exit.example', 'port': 8443},
      ],
    });
    expect(n.isChain, true);
    expect(n.chain.length, 2);
    expect(n.title, 'first.example → exit.example');
    expect(NodeDto.fromJson({'id': 'x', 'proto': 'vless', 'server': 's', 'port': 1}).isChain, false);
    expect(NodeDto.fromJson({'id': 'c2', 'proto': 'chain', 'label': 'Мой', 'chain': []}).title, 'Мой');
  });

  test('кандидаты в звенья: свои первыми, WireGuard есть, цепочек нет, без повторов', () async {
    final m = AppModel(_Api());
    await m.loadOwn();
    await m.loadNodes();
    final ids = chainCandidates(m).map((n) => n.id).toList();
    expect(ids.take(2), ['own1', 'own2'], reason: 'свои серверы идут первыми');
    expect(ids.contains('wg1'), true, reason: 'WireGuard можно выбрать, но только первым звеном (диалог блокирует остальные места)');
    expect(ids.contains('chain1'), false);
    expect(ids.toSet().length, ids.length);
    expect(ids.contains('free1'), true);
  });

  testWidgets('диалог: нужно два звена, порядок выбора сохраняется, максимум три', (t) async {
    t.view.physicalSize = const Size(900, 1600); // весь список диалога умещается без прокрутки
    t.view.devicePixelRatio = 1;
    addTearDown(t.view.reset);
    final api = _Api();
    final m = AppModel(api);
    await m.loadOwn();
    await m.loadNodes();
    await t.pumpWidget(MaterialApp(home: Builder(builder: (ctx) => TextButton(onPressed: () => chainDialog(ctx, m), child: const Text('открыть')))));
    await t.tap(find.text('открыть'));
    await t.pumpAndSettle();
    final create = find.widgetWithText(FilledButton, 'Создать');
    expect(tester(t, create).onPressed, isNull, reason: 'без звеньев создать нельзя');
    await t.tap(find.text('vps2.example.org:443'));
    await t.pump();
    expect(tester(t, create).onPressed, isNull, reason: 'одно звено — мало');
    await t.tap(find.text('vps.example.org:443'));
    await t.pump();
    expect(tester(t, create).onPressed, isNotNull);
    final free = find.text('free.example.net:443');
    await t.scrollUntilVisible(free, 60, scrollable: find.byType(Scrollable).last);
    await t.pumpAndSettle();
    await t.tap(free);
    await t.pump();
    await t.tap(create);
    await t.pumpAndSettle();
    expect(api.created.length, 1);
    expect(api.created.single.$2, ['own2', 'own1', 'free1'], reason: 'порядок звеньев — порядок выбора');
  });

  testWidgets('диалог: WireGuard выбирается только первым звеном', (t) async {
    t.view.physicalSize = const Size(900, 1600);
    t.view.devicePixelRatio = 1;
    addTearDown(t.view.reset);
    final api = _Api();
    final m = AppModel(api);
    await m.loadOwn();
    await m.loadNodes();
    await t.pumpWidget(MaterialApp(home: Builder(builder: (ctx) => TextButton(onPressed: () => chainDialog(ctx, m), child: const Text('открыть')))));
    await t.tap(find.text('открыть'));
    await t.pumpAndSettle();
    final wg = find.text('wg.example.net:443');
    final list = find.byType(Scrollable).last;
    await t.scrollUntilVisible(wg, 60, scrollable: list);
    await t.pumpAndSettle();
    // сначала обычное звено: WireGuard на второе место не встаёт
    await t.scrollUntilVisible(find.text('vps.example.org:443'), -60, scrollable: list);
    await t.tap(find.text('vps.example.org:443'));
    await t.pump();
    await t.scrollUntilVisible(wg, 60, scrollable: list);
    await t.tap(wg, warnIfMissed: false);
    await t.pump();
    expect(tester(t, find.widgetWithText(FilledButton, 'Создать')).onPressed, isNull, reason: 'WireGuard вторым звеном не добавился: звено всё ещё одно');
    // убрали первое звено: теперь WireGuard встаёт первым, и цепочка собирается со вторым обычным
    t.widget<InputChip>(find.byType(InputChip).first).onDeleted!(); // убрали первое звено
    await t.pump();
    await t.scrollUntilVisible(wg, 60, scrollable: list);
    await t.tap(wg);
    await t.pump();
    await t.scrollUntilVisible(find.text('vps2.example.org:443'), -60, scrollable: list);
    await t.tap(find.text('vps2.example.org:443'));
    await t.pump();
    await t.tap(find.widgetWithText(FilledButton, 'Создать'));
    await t.pumpAndSettle();
    expect(api.created.single.$2, ['wg1', 'own2']);
  });

  testWidgets('проверка звеньев: видно, какое звено не отвечает, и есть путь собрать цепочку заново', (t) async {
    final m = AppModel(_Api());
    await t.pumpWidget(MaterialApp(home: Scaffold(body: Builder(builder: (c) => TextButton(onPressed: () => chainCheckDialog(c, m, node('chain1', 'chain', '')), child: const Text('go'))))));
    await t.tap(find.text('go'));
    await t.pumpAndSettle();
    expect(find.textContaining('1. a.example:443'), findsOneWidget);
    expect(find.textContaining('отвечает · 40 мс'), findsOneWidget);
    expect(find.textContaining('не отвечает: connection refused'), findsOneWidget);
    expect(find.textContaining('на UDP, напрямую не проверяется'), findsOneWidget);
    expect(find.textContaining('Не отвечает звено 2'), findsOneWidget);
    expect(find.text('Собрать заново'), findsOneWidget);
    m.dispose();
  });
}

FilledButton tester(WidgetTester t, Finder f) => t.widget<FilledButton>(f);
