import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/country.dart';
import 'package:brod/src/rules_page.dart';

const _nodeId = 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678';

void main() {
  setUp(() => flagEmojiOverride = false);
  tearDown(() => flagEmojiOverride = null);

  test('выход правила: туда и обратно в JSON; без proxy выход не отправляется', () {
    final r = RouteRuleDto(
        action: 'proxy', match: RouteMatch(domainSuffix: ['nl.example']), via: RouteViaDto(kind: 'country', country: 'NL', fallback: 'direct'));
    final j = r.toJson();
    expect(j['via'], {'kind': 'country', 'country': 'NL', 'fallback': 'direct'});
    final back = RouteRuleDto.fromJson(j);
    expect(back.via?.kind, 'country');
    expect(back.via?.country, 'NL');
    expect(back.via?.fallback, 'direct');
    // страна у вида «узел» и узел у вида «страна» не уходят на сервер
    expect(RouteViaDto(kind: 'node', node: _nodeId, country: 'NL').toJson(), {'kind': 'node', 'node': _nodeId});
    // действие direct: via игнорируется (сервер такое отклонил бы)
    final d = RouteRuleDto(action: 'direct', match: RouteMatch(domain: ['a.example']), via: RouteViaDto(kind: 'warp'));
    expect(d.toJson().containsKey('via'), isFalse);
    expect(RouteRuleDto.fromJson({'id': 'x', 'action': 'proxy', 'match': <String, dynamic>{}}).via, isNull);
  });

  test('подпись выхода в списке правил', () {
    final r = RouteRuleDto(id: 'r1', action: 'proxy', via: RouteViaDto(kind: 'country', country: 'NL'));
    expect(viaSummary(r), contains(countryName('NL')));
    expect(viaSummary(RouteRuleDto(action: 'proxy')), '');
    expect(viaSummary(RouteRuleDto(action: 'direct', via: RouteViaDto(kind: 'warp'))), '');
    expect(viaSummary(RouteRuleDto(action: 'proxy', via: RouteViaDto(kind: 'warp'))), 'WARP');
    final nodes = [const NodeDto(id: _nodeId, proto: 'vless', server: 'host.example', port: 443, exit: ExitDto(ip: '1.1.1.1', country: 'DE'))];
    final byNode = RouteRuleDto(id: 'r2', action: 'proxy', via: RouteViaDto(kind: 'node', node: _nodeId));
    expect(viaSummary(byNode, nodes: nodes), contains('host.example:443'));
    expect(viaSummary(byNode), contains(_nodeId.substring(0, 8)), reason: 'узла нет в загруженном списке — короткий идентификатор');
    final st = const ViaStateDto(rule: 'r1', target: 'none', fallback: true);
    expect(viaSummary(r, state: st), contains('нет подходящего: блок'));
    final live = const ViaStateDto(rule: 'r1', target: 'node', node: _nodeId);
    expect(viaSummary(r, state: live, nodes: nodes), contains('сейчас'));
  });

  testWidgets('диалог правила: выбор страны и узла возвращает via', (tester) async {
    RouteRuleDto? result;
    final nodes = [const NodeDto(id: _nodeId, proto: 'vless', server: 'host.example', port: 443, l2At: null, exit: ExitDto(ip: '1.1.1.1', country: 'DE'))];
    await tester.pumpWidget(MaterialApp(
      home: Builder(
        builder: (context) => Scaffold(
          body: TextButton(
            onPressed: () async {
              result = await showDialog<RouteRuleDto>(
                context: context,
                builder: (_) => RuleDialog(
                  initial: RouteRuleDto(match: RouteMatch(domainSuffix: ['x.example'])),
                  sets: const [],
                  countries: const [CountryDto('NL', 2, 3), CountryDto('DE', 0, 1)],
                  nodes: nodes,
                ),
              );
            },
            child: const Text('open'),
          ),
        ),
      ),
    ));
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.text('Выход'), findsOneWidget);

    // страна без выбора не принимается
    await tester.tap(find.text('Страна'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Готово'));
    await tester.pumpAndSettle();
    expect(find.text('Выберите страну'), findsOneWidget);

    await tester.tap(find.byType(DropdownButtonFormField<String>).first);
    await tester.pumpAndSettle();
    await tester.tap(find.textContaining('живых 2 из 3').last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Готово'));
    await tester.pumpAndSettle();
    expect(result?.via?.kind, 'country');
    expect(result?.via?.country, 'NL');
    expect(result?.via?.fallback, '');

    // узел
    result = null;
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Узел'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Выбрать узел…'));
    await tester.pumpAndSettle();
    await tester.tap(find.textContaining('host.example'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Готово'));
    await tester.pumpAndSettle();
    expect(result?.via?.kind, 'node');
    expect(result?.via?.node, _nodeId);

    // «Авто»: собственного выхода у правила нет
    result = null;
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Готово'));
    await tester.pumpAndSettle();
    expect(result, isNotNull);
    expect(result?.via, isNull);
  });

  test('новые условия: JSON, пустота, подпись', () {
    final m = RouteMatch(
        ipVersion: 6,
        protocol: ['tls', 'quic'],
        sourceIpCidr: ['192.168.1.0/24'],
        ipIsPrivate: true,
        sourcePort: [5000],
        sourcePortRange: ['6000:6100'],
        processPath: ['/usr/bin/curl'],
        processPathRegex: [r'^/opt/.*$'],
        packageName: ['com.example.app'],
        invert: true);
    final j = m.toJson();
    expect(j['ip_version'], 6);
    expect(j['protocol'], ['tls', 'quic']);
    expect(j['invert'], true);
    final back = RouteMatch.fromJson(j);
    expect(back.toJson(), j);
    expect(back.ipIsPrivate, isTrue);
    expect(back.sourcePort, [5000]);
    expect(back.packageName, ['com.example.app']);
    expect(back.summary, startsWith('НЕ ('), reason: 'инвертированное правило видно в списке');
    // инверсия сама по себе не условие
    expect(RouteMatch(invert: true).isEmpty, isTrue);
    expect(RouteMatch(ipIsPrivate: true).isEmpty, isFalse);
    expect(RouteMatch(ipVersion: 4).isEmpty, isFalse);
    // способ блокировки уходит на сервер только у block
    expect(RouteRuleDto(action: 'block', blockMethod: 'drop', match: RouteMatch(domain: ['a.example'])).toJson()['block_method'], 'drop');
    expect(RouteRuleDto(action: 'direct', blockMethod: 'drop', match: RouteMatch(domain: ['a.example'])).toJson().containsKey('block_method'), isFalse);
  });

  testWidgets('диалог правила: дополнительные условия и способ блокировки', (tester) async {
    RouteRuleDto? result;
    await tester.binding.setSurfaceSize(const Size(900, 1400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    Future<void> open(RouteRuleDto initial) async {
      await tester.pumpWidget(MaterialApp(
        home: Builder(
          builder: (context) => Scaffold(
            body: TextButton(
              onPressed: () async => result = await showDialog<RouteRuleDto>(context: context, builder: (_) => RuleDialog(initial: initial, sets: const [])),
              child: const Text('open'),
            ),
          ),
        ),
      ));
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
    }

    await open(RouteRuleDto(action: 'block', match: RouteMatch(domainSuffix: ['ads.example'])));
    await tester.tap(find.text('молча'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Дополнительные условия'));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.text('IPv6'));
    await tester.tap(find.text('IPv6'));
    await tester.ensureVisible(find.text('tls'));
    await tester.tap(find.text('tls'));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.text('Инвертировать'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Инвертировать'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Готово'));
    await tester.pumpAndSettle();
    expect(result?.blockMethod, 'drop');
    expect(result?.match.ipVersion, 6);
    expect(result?.match.protocol, ['tls']);
    expect(result?.match.invert, isTrue);

    // только инверсия без условий — правило без условий
    await open(RouteRuleDto(action: 'direct'));
    await tester.tap(find.text('Дополнительные условия'));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.text('Инвертировать'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Инвертировать'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Готово'));
    await tester.pumpAndSettle();
    expect(find.text('Задайте хотя бы одно условие'), findsOneWidget);
  });
}
