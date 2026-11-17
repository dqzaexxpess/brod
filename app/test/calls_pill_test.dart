import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/widgets.dart';

Widget host(NodeDto n) => MaterialApp(home: Scaffold(body: NodePills(node: n)));

void main() {
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
