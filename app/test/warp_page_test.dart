import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/warp_page.dart';

class _WarpApi implements DaemonApi {
  WarpStateDto state = const WarpStateDto(enabled: true, current: WarpProfileDto(endpoint: '162.159.192.1', port: 2408, mask: 'quic'));
  final calls = <String>[];
  Map<String, String>? applied;

  @override
  Future<WarpStateDto> warpState() async => state;

  @override
  Future<List<WarpScanResultDto>> warpScan({bool apply = true, int stopAtGood = 0}) async {
    calls.add('scan');
    state = const WarpStateDto(
        enabled: true,
        current: WarpProfileDto(endpoint: '188.114.97.9', port: 987, mask: 'quic'),
        remembered: WarpProfileDto(endpoint: '188.114.97.9', port: 987, mask: 'quic'));
    return const [
      WarpScanResultDto(profile: WarpProfileDto(endpoint: '162.159.192.1', port: 2408, mask: 'quic'), cls: 'timeout'),
      WarpScanResultDto(profile: WarpProfileDto(endpoint: '188.114.97.9', port: 987, mask: 'quic'), ok: true, latencyMs: 406),
      WarpScanResultDto(profile: WarpProfileDto(endpoint: '188.114.96.74', port: 2408, mask: 'none'), ok: true, latencyMs: 395),
    ];
  }

  @override
  Future<void> warpParams(Map<String, String> params) async {
    calls.add('params');
    applied = params;
    state = WarpStateDto(enabled: true, manual: {for (final e in params.entries) if (e.value.isNotEmpty) e.key: e.value});
  }

  @override
  Future<void> warpReset() async => calls.add('reset');

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  test('данные: состояние WARP и итог подбора читаются из JSON', () {
    final s = WarpStateDto.fromJson({
      'enabled': true,
      'auto': false,
      'current': {'endpoint': '1.2.3.4', 'port': 987, 'mask': 'none'},
      'remembered': {'endpoint': '5.6.7.8', 'port': 2408, 'mask': 'quic', 'mtu': 1380},
      'manual': {'warp.port': '987'},
    });
    expect(s.enabled && !s.auto, isTrue);
    expect('${s.current}', '1.2.3.4:987 · без маскировки');
    expect(s.remembered!.mtu, 1380);
    expect(s.manual['warp.port'], '987');
    final r = WarpScanResultDto.fromJson({'profile': {'endpoint': 'h', 'port': 1, 'mask': 'quic'}, 'ok': true, 'latency_ms': 400});
    expect(r.ok && r.latencyMs == 400, isTrue);
  });

  testWidgets('окно WARP: подбор показывает лучшие профили, ручные параметры уходят одной записью', (tester) async {
    await tester.binding.setSurfaceSize(const Size(900, 1600));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _WarpApi();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await tester.pumpWidget(MaterialApp(home: WarpPage(model: m)));
    await tester.pumpAndSettle();
    expect(find.textContaining('Аккаунт настроен'), findsOneWidget);
    expect(find.textContaining('162.159.192.1:2408'), findsOneWidget);
    await tester.tap(find.text('Подобрать для этой сети'));
    await tester.pumpAndSettle();
    expect(api.calls, contains('scan'));
    expect(find.textContaining('рабочих: 2'), findsOneWidget);
    expect(find.text('395 мс'), findsOneWidget);
    expect(find.textContaining('Запомнено для этой сети: 188.114.97.9:987'), findsOneWidget);
    await tester.enterText(find.widgetWithText(TextField, 'Адрес'), '188.114.96.74');
    await tester.enterText(find.widgetWithText(TextField, 'Порт'), '2408');
    await tester.tap(find.text('Применить'));
    await tester.pumpAndSettle();
    expect(api.applied!['warp.endpoint'], '188.114.96.74');
    expect(api.applied!['warp.port'], '2408');
    expect(api.applied!['warp.mtu'], '');
    m.dispose();
  });
}
