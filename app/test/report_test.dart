import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/report_page.dart';

class _Api implements DaemonApi {
  ReportStateDto next = const ReportStateDto(state: 'idle');
  int starts = 0;

  @override
  Future<void> reportStart() async => starts++;

  @override
  Future<ReportStateDto> reportState() async => next;

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  udpTest();
  test('данные: итог отчёта читается из JSON ядра', () {
    final s = ReportStateDto.fromJson({
      'state': 'done',
      'stage': 'пробы узлов',
      'text': 'Brod 0.4.2',
      'report': {
        'findings': ['Системный DNS не отвечает', 'Нет прямого TCP']
      },
    });
    expect(s.done, true);
    expect(s.running, false);
    expect(s.findings, ['Системный DNS не отвечает', 'Нет прямого TCP']);
    expect(ReportStateDto.fromJson({'state': 'running', 'stage': 'DNS'}).running, true);
    expect(ReportStateDto.fromJson({}).state, 'idle');
  });

  test('модель: отчёт запускается и ждёт итога, повторный запуск во время работы игнорируется', () async {
    final api = _Api()..next = const ReportStateDto(state: 'done', text: 'итог', json: {'findings': ['x']});
    final m = AppModel(api);
    final f = m.runReport();
    await m.runReport(); // второй вызов ничего не делает
    await f;
    expect(api.starts, 1);
    expect(m.report?.done, true);
    expect(m.reportRunning, false);
  });

  testWidgets('страница: кнопка запуска, выводы после итога', (t) async {
    final api = _Api();
    final m = AppModel(api);
    await t.pumpWidget(MaterialApp(home: ReportPage(model: m)));
    expect(find.text('Запустить тесты'), findsOneWidget);
    m.report = const ReportStateDto(state: 'done', text: 'x', json: {'findings': ['Системный DNS не отвечает нормально']});
    // ignore: invalid_use_of_protected_member
    m.notifyListeners();
    await t.pump();
    expect(find.text('Выводы'), findsOneWidget);
    expect(find.text('Системный DNS не отвечает нормально'), findsOneWidget);
  });
}

void udpTest() {
  test('статус: минуты простоя WARP читаются из ответа демона', () {
    final s = DaemonStatus.fromJson({
      'version': '0.4.4',
      'nodes': 1,
      'present': 1,
      'warp': {'enabled': true, 'via_warp': false, 'down_min': 12},
    });
    expect(s.warpEnabled, true);
    expect(s.warpDownMin, 12);
    expect(DaemonStatus.fromJson({'version': 'x', 'nodes': 0, 'present': 0}).warpDownMin, 0);
  });
}
