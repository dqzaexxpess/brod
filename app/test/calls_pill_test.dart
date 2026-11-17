import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/widgets.dart';

Widget host(NodeDto n) => MaterialApp(home: Scaffold(body: NodePills(node: n)));

void main() {
  homePillsTests();
  test('замер UDP из ответа ядра', () {
    final n = NodeDto.fromJson({'id': 'a', 'udp': 'good', 'udp_ms': 74});
    expect(n.udp, 'good');
    expect(n.udpMs, 74);
    expect(NodeDto.fromJson({'id': 'a'}).udp, '');
    expect(n.signature, isNot(NodeDto.fromJson({'id': 'a', 'udp': 'none'}).signature), reason: 'смена метки должна перерисовать строку');
  });

  testWidgets('метка «для звонков» по результатам замера', (t) async {
    await t.pumpWidget(host(const NodeDto(id: 'a', proto: 'vless', server: 's', port: 1, udp: 'good')));
    expect(find.text('для звонков'), findsOneWidget);
    await t.pumpWidget(host(const NodeDto(id: 'a', proto: 'vless', server: 's', port: 1, udp: 'unstable')));
    expect(find.text('звонки рвутся'), findsOneWidget);
    await t.pumpWidget(host(const NodeDto(id: 'a', proto: 'vless', server: 's', port: 1, udp: 'none')));
    expect(find.text('нет UDP'), findsOneWidget);
    await t.pumpWidget(host(const NodeDto(id: 'a', proto: 'vless', server: 's', port: 1)));
    expect(find.text('для звонков'), findsNothing, reason: 'без замера метки нет');
  });
}

void homePillsTests() {
  Widget pills(NodeDto n, {bool showPing = true, List<Widget> leading = const []}) =>
      MaterialApp(home: Scaffold(body: NodePills(node: n, showPing: showPing, leading: leading)));

  testWidgets('главная: пинг не повторяется, когда есть таблетка выхода; остальные таблетки на месте', (t) async {
    const n = NodeDto(id: 'a', proto: 'vless', server: 's', port: 1, latencyMs: 409, speedKBps: 3072, youtube: true, insecure: true, own: true, udp: 'good');
    await t.pumpWidget(pills(n, showPing: false, leading: const [Pill(text: 'выход работает · 409 мс')]));
    expect(find.text('выход работает · 409 мс'), findsOneWidget);
    expect(find.text('409 мс'), findsNothing, reason: 'пинг показывается только внутри таблетки выхода');
    for (final label in ['YouTube', 'для звонков', 'мой', 'без проверки TLS']) {
      expect(find.text(label), findsOneWidget, reason: label);
    }
    expect(find.textContaining('Мбит/с'), findsOneWidget);
  });

  testWidgets('без проверки выхода пинг показывается отдельной таблеткой', (t) async {
    await t.pumpWidget(pills(const NodeDto(id: 'a', proto: 'vless', server: 's', port: 1, latencyMs: 409)));
    expect(find.text('409 мс'), findsOneWidget);
  });

  test('активный узел несёт YouTube, «мой» и «без проверки TLS»', () {
    const a = ActiveNode(proto: 'vless', server: 's', port: 1, youtube: true, own: true, insecure: true);
    expect(a == const ActiveNode(proto: 'vless', server: 's', port: 1, youtube: true, own: true, insecure: true), isTrue);
    expect(a == const ActiveNode(proto: 'vless', server: 's', port: 1), isFalse);
  });
}
