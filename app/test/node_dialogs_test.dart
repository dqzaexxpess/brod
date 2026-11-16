// диалоги переименования узла и правки своего сервера (node_dialogs.dart): действия доходят до API,
// отмена ничего не меняет, отказ API показывается в окне и не закрывает его.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/node_dialogs.dart';

class _Api implements DaemonApi {
  final calls = <String>[];
  String? editError;

  @override
  Future<void> setLabel(String id, String label) async => calls.add('label:$id:$label');

  @override
  Future<String> editOwn(String id, {String? label, String? server, int? port, String? link}) async {
    calls.add('edit:$id|label=$label|server=$server|port=$port|link=$link');
    if (editError != null) throw ApiException(400, editError!);
    return id;
  }

  @override
  Future<List<NodeDto>> nodes({bool alive = false, bool favorites = false, int limit = 300, String source = ''}) async => [];

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

const _node = NodeDto(id: 'o1', proto: 'vless', server: 'vps.example', port: 443, own: true, label: 'Мой');

Future<AppModel> _open(WidgetTester tester, _Api api, Future<void> Function(BuildContext, AppModel) dialog) async {
  final m = AppModel(api, pollEvery: const Duration(hours: 1));
  await tester.binding.setSurfaceSize(const Size(900, 1600));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(MaterialApp(
    home: Builder(builder: (context) => Scaffold(body: TextButton(onPressed: () => dialog(context, m), child: const Text('открыть')))),
  ));
  await tester.tap(find.text('открыть'));
  await tester.pumpAndSettle();
  return m;
}

void main() {
  testWidgets('переименование: текущее название в поле, «Сохранить» отправляет обрезанный текст', (tester) async {
    final api = _Api();
    await _open(tester, api, (c, m) => renameNodeDialog(c, m, _node));
    expect(find.text('Название узла'), findsOneWidget);
    expect(find.widgetWithText(TextField, 'Мой'), findsOneWidget);
    await tester.enterText(find.byType(TextField), '  Новый  ');
    await tester.tap(find.text('Сохранить'));
    await tester.pumpAndSettle();
    expect(api.calls, ['label:o1:Новый']);
    expect(find.text('Название узла'), findsNothing);
  });

  testWidgets('переименование: пустое название отправляется как пустое (вернуть адрес), «Отмена» ничего не шлёт', (tester) async {
    final api = _Api();
    await _open(tester, api, (c, m) => renameNodeDialog(c, m, _node));
    await tester.enterText(find.byType(TextField), '');
    await tester.tap(find.text('Сохранить'));
    await tester.pumpAndSettle();
    expect(api.calls, ['label:o1:']);

    final api2 = _Api();
    await _open(tester, api2, (c, m) => renameNodeDialog(c, m, _node));
    await tester.enterText(find.byType(TextField), 'другое');
    await tester.tap(find.text('Отмена'));
    await tester.pumpAndSettle();
    expect(api2.calls, isEmpty);
    expect(find.text('Название узла'), findsNothing);
  });

  testWidgets('правка своего сервера: отправляются только изменённые поля', (tester) async {
    final api = _Api();
    await _open(tester, api, (c, m) => editOwnDialog(c, m, _node));
    expect(find.text('Изменить сервер'), findsOneWidget);
    await tester.enterText(find.widgetWithText(TextField, 'Мой'), 'Другой');
    await tester.enterText(find.widgetWithText(TextField, '443'), '8443');
    await tester.tap(find.text('Сохранить'));
    await tester.pumpAndSettle();
    expect(api.calls.first, 'edit:o1|label=Другой|server=null|port=8443|link=null');
    expect(find.text('Изменить сервер'), findsNothing);
  });

  testWidgets('правка без изменений шлёт пустой запрос; ссылка отменяет правку адреса и порта', (tester) async {
    final api = _Api();
    await _open(tester, api, (c, m) => editOwnDialog(c, m, _node));
    await tester.tap(find.text('Сохранить'));
    await tester.pumpAndSettle();
    expect(api.calls.first, 'edit:o1|label=null|server=null|port=null|link=null');

    final api2 = _Api();
    await _open(tester, api2, (c, m) => editOwnDialog(c, m, _node));
    await tester.enterText(find.widgetWithText(TextField, 'vps.example'), 'other.example');
    await tester.tap(find.text('Заменить ссылкой'));
    await tester.pumpAndSettle();
    await tester.enterText(find.widgetWithText(TextField, 'vless://… или ss://…'), ' vless://u@h:1 ');
    await tester.tap(find.text('Сохранить'));
    await tester.pumpAndSettle();
    expect(api2.calls.first, 'edit:o1|label=null|server=null|port=null|link=vless://u@h:1');
  });

  testWidgets('отказ API: сообщение в окне, окно остаётся открытым; «Отмена» закрывает без запроса', (tester) async {
    final api = _Api()..editError = 'нельзя изменить';
    await _open(tester, api, (c, m) => editOwnDialog(c, m, _node));
    await tester.enterText(find.widgetWithText(TextField, '443'), '1');
    await tester.tap(find.text('Сохранить'));
    await tester.pumpAndSettle();
    expect(find.text('нельзя изменить'), findsOneWidget);
    expect(find.text('Изменить сервер'), findsOneWidget);
    await tester.tap(find.text('Отмена'));
    await tester.pumpAndSettle();
    expect(find.text('Изменить сервер'), findsNothing);
    expect(api.calls.length, 1);
  });
}
