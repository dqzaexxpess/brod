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

  test('кандидаты в звенья: свои первыми, без WireGuard и цепочек, без повторов', () async {
    final m = AppModel(_Api());
    await m.loadOwn();
    await m.loadNodes();
    final ids = chainCandidates(m).map((n) => n.id).toList();
    expect(ids.take(2), ['own1', 'own2'], reason: 'свои серверы идут первыми');
    expect(ids.contains('wg1'), false);
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
    await t.ensureVisible(free);
    await t.pumpAndSettle();
    await t.tap(free);
    await t.pump();
    await t.tap(create);
    await t.pumpAndSettle();
    expect(api.created.length, 1);
    expect(api.created.single.$2, ['own2', 'own1', 'free1'], reason: 'порядок звеньев — порядок выбора');
  });
}

FilledButton tester(WidgetTester t, Finder f) => t.widget<FilledButton>(f);
