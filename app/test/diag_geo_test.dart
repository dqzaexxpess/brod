import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/extras.dart';
import 'package:brod/src/model.dart';

class _Api implements DaemonApi {
  @override
  Future<DiagDto> diagnostics() async => DiagDto.fromJson({
        'ip': '203.0.113.9',
        'country': 'NL',
        'vpn_on': true,
        'mode': 'all',
        'geo': [
          {'name': 'ipinfo', 'country': 'DE', 'city': 'Frankfurt', 'org': 'AS1 Hetzner', 'timezone': 'Europe/Berlin'},
          {'name': 'ip-api', 'country': 'NL', 'city': 'Amsterdam', 'hosting': true, 'proxy': false},
          {'name': 'ipwho.is', 'error': 'HTTP 429'},
        ],
        'findings': [
          {'level': 'warn', 'title': 'Базы геолокации расходятся по стране', 'detail': 'ipinfo: DE · ip-api: NL'},
        ],
      });

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  test('диагностика: базы геолокации разбираются', () async {
    final d = await _Api().diagnostics();
    expect(d.geo.length, 3);
    expect(d.geo[1].hosting, isTrue);
    expect(d.geo[1].proxy, isFalse);
    expect(d.geo[2].error, 'HTTP 429');
  });

  testWidgets('диагностика: расхождение баз видно на экране', (tester) async {
    await tester.binding.setSurfaceSize(const Size(900, 1600));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final m = AppModel(_Api(), pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(MaterialApp(home: DiagnosticsPage(model: m)));
    await tester.pumpAndSettle();
    expect(find.text('Что говорят базы геолокации'), findsOneWidget);
    expect(find.text('ipinfo'), findsOneWidget);
    expect(find.textContaining('Frankfurt'), findsOneWidget);
    expect(find.text('дата-центр'), findsOneWidget);
    expect(find.textContaining('нет ответа (HTTP 429)'), findsOneWidget);
    expect(find.text('Базы геолокации расходятся по стране'), findsOneWidget);
    m.dispose();
  });
}
