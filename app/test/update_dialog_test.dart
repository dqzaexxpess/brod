import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/update_dialog.dart';

class _UpdApi implements DaemonApi {
  UpdateDto state = const UpdateDto(current: '0.3.0', platform: 'linux-amd64');
  UpdateDto afterCheck = UpdateDto(current: '0.3.0', platform: 'linux-amd64', checked: DateTime(2026, 11, 10), available: true, app: '0.4.0', notes: 'правила маршрутизации', size: 91 << 20, sha256: 'ab' * 32);
  UpdateDto afterDownload = UpdateDto(
      current: '0.3.0',
      platform: 'linux-amd64',
      checked: DateTime(2026, 11, 10),
      available: true,
      app: '0.4.0',
      size: 91 << 20,
      download: const UpdateDownloadDto(state: 'ready', bytes: 91 << 20, total: 91 << 20, path: '/data/updates/brod-0.4.0-linux-amd64.tar.gz'));
  final calls = <String>[];

  @override
  Future<UpdateDto> update() async {
    calls.add('update');
    return state;
  }

  @override
  Future<UpdateDto> checkUpdate() async {
    calls.add('check');
    return state = afterCheck;
  }

  int installPid = -1;

  @override
  Future<UpdateDto> installUpdate({int appPid = 0}) async {
    calls.add('install');
    installPid = appPid;
    return state = UpdateDto(current: '0.3.0', platform: 'linux-amd64', available: true, app: '0.4.0', installing: true, download: afterDownload.download);
  }

  @override
  Future<UpdateDto> downloadUpdate({bool allowMetered = false}) async {
    calls.add(allowMetered ? 'download metered' : 'download');
    return state = afterDownload;
  }

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

Future<void> _open(WidgetTester tester, AppModel m) async {
  await tester.binding.setSurfaceSize(const Size(900, 1400));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(MaterialApp(
    home: Builder(builder: (context) => Scaffold(body: TextButton(onPressed: () => showUpdateDialog(context, m), child: const Text('open')))),
  ));
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

void main() {
  test('данные: обновление и краткая сводка в статусе', () {
    final u = UpdateDto.fromJson({
      'current': '0.3.0',
      'platform': 'linux-amd64',
      'checked': '2026-11-10T12:00:00Z',
      'available': true,
      'app': '0.4.0',
      'size': 100,
      'download': {'state': 'downloading', 'bytes': 40, 'total': 100}
    });
    expect(u.available, isTrue);
    expect(u.checked, isNotNull);
    expect(u.download.downloading, isTrue);
    expect(u.download.bytes, 40);
    expect(UpdateDto.fromJson(<String, dynamic>{'current': '0.3.0'}).checked, isNull, reason: 'проверки ещё не было');
    final st = DaemonStatus.fromJson({'version': '0.3.0-linux', 'nodes': 1, 'present': 1, 'update': {'available': true, 'app': '0.4.0', 'download': 'ready'}});
    expect(st.update?.app, '0.4.0');
    expect(st.update?.download, 'ready');
    expect(DaemonStatus.fromJson({'version': 'x'}).update, isNull);
    expect(st == DaemonStatus.fromJson({'version': '0.3.0-linux', 'nodes': 1, 'present': 1}), isFalse, reason: 'появление блока update меняет статус (перерисовка плашки)');
  });

  test('сырые ошибки превращаются в понятные фразы', () {
    expect(friendlyUpdateError('Get "https://x": dial tcp: lookup x: no such host'), contains('DNS'));
    expect(friendlyUpdateError('read tcp 1.2.3.4:1->5.6.7.8:443: read: connection reset by peer'), contains('не отвечает'));
    expect(friendlyUpdateError('x.example: HTTP 503'), contains('HTTP 503'));
    expect(friendlyUpdateError('манифест релизов отвергнут: подпись не сходится'), contains('подписи'));
    expect(friendlyUpdateError('что-то странное'), 'Проверка не удалась.');
  });

  test('подсказки по установке разные на платформах', () {
    expect(updateInstallHint(android: true), contains('кнопкой'));
    expect(updateInstallHint(android: false, windows: true), contains('установщик'));
    expect(updateInstallHint(android: false, windows: false), contains('распакуйте архив'));
  });

  testWidgets('диалог: проверка находит версию, скачивание завершается проверенным файлом', (tester) async {
    final api = _UpdApi();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, m);
    expect(find.text('Установлена версия 0.3.0 (linux-amd64)'), findsOneWidget);
    expect(find.textContaining('Доступна версия'), findsNothing);
    await tester.tap(find.text('Проверить сейчас'));
    await tester.pumpAndSettle();
    expect(find.text('Доступна версия 0.4.0'), findsOneWidget);
    expect(find.text('правила маршрутизации'), findsOneWidget);
    expect(find.textContaining('91 МБ'), findsOneWidget);
    await tester.tap(find.text('Скачать и проверить'));
    await tester.pumpAndSettle();
    expect(api.calls, containsAllInOrder(['check', 'download']));
    expect(find.textContaining('Файл скачан и проверен'), findsOneWidget);
    expect(find.text('/data/updates/brod-0.4.0-linux-amd64.tar.gz'), findsOneWidget);
    expect(find.text('Скачать и проверить'), findsNothing);
    m.dispose();
  });

  testWidgets('диалог: в мобильной сети скачивание предлагается с предупреждением и размером, согласие передаётся ядру', (tester) async {
    final api = _UpdApi();
    api.state = UpdateDto(
        current: '0.3.0', platform: 'android-arm64', checked: DateTime(2026, 11, 10), available: true, app: '0.4.0', size: 91 << 20, metered: true);
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, m);
    expect(find.textContaining('мобильная сеть'), findsOneWidget);
    expect(find.text('Скачать и проверить'), findsNothing);
    await tester.tap(find.textContaining('Скачать по мобильной сети'));
    await tester.pumpAndSettle();
    expect(api.calls, contains('download metered'));
    m.dispose();
  });

  testWidgets('диалог: кнопка «Установить» передаёт проверенный файл установщику; без разрешения система просит его', (tester) async {
    final api = _UpdApi();
    api.state = api.afterDownload;
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    final installed = <String>[];
    var answer = 'permission';
    m.installSupported = true;
    m.installViaSystem = true;
    m.installApk = (p) async {
      installed.add(p);
      if (answer == 'boom') throw Exception('нет установщика');
      return answer;
    };
    await _open(tester, m);
    expect(find.text('Установить'), findsOneWidget);
    await tester.tap(find.text('Установить'));
    await tester.pumpAndSettle();
    expect(installed, ['/data/updates/brod-0.4.0-linux-amd64.tar.gz']);
    expect(find.textContaining('Разрешите Brod устанавливать приложения'), findsOneWidget);
    answer = 'started';
    await tester.tap(find.text('Установить'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Окно установки открыто'), findsOneWidget);
    expect(find.textContaining('Разрешите Brod'), findsNothing);
    answer = 'boom';
    await tester.tap(find.text('Установить'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Установка не удалась'), findsOneWidget);
    expect(installed.length, 3);
    m.dispose();
  });

  testWidgets('диалог: на Linux и Windows «Установить» просит ядро, затем приложение закрывается', (tester) async {
    final api = _UpdApi();
    api.state = api.afterDownload;
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    var quit = 0;
    m.installSupported = true;
    m.onQuitForInstall = () async => quit++;
    await _open(tester, m);
    await tester.tap(find.text('Установить'));
    await tester.pump();
    await tester.pump();
    expect(api.calls, contains('install'));
    expect(api.installPid, greaterThan(0), reason: 'ядро ждёт выхода приложения по PID');
    expect(find.textContaining('Устанавливаю: приложение закроется'), findsOneWidget);
    expect(quit, 0);
    await tester.pump(const Duration(seconds: 1));
    expect(quit, 1);
    m.dispose();
  });

  testWidgets('диалог: итог последней установки виден после перезапуска', (tester) async {
    final api = _UpdApi();
    api.state = UpdateDto.fromJson({
      'current': '0.4.0',
      'platform': 'linux-amd64',
      'last': {'from': '0.3.0', 'to': '0.4.0', 'ok': false, 'error': 'права для режима TUN не выданы', 'at': '2026-11-10T12:00:00Z'}
    });
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, m);
    expect(find.textContaining('Последняя установка 0.3.0 → 0.4.0 не удалась: права для режима TUN не выданы'), findsOneWidget);
    m.dispose();
  });

  testWidgets('диалог: ход скачивания и повтор после сбоя', (tester) async {
    final api = _UpdApi();
    api.state = api.afterCheck;
    api.afterDownload = UpdateDto(
        current: '0.3.0', platform: 'linux-amd64', available: true, app: '0.4.0', size: 100, download: const UpdateDownloadDto(state: 'downloading', bytes: 25, total: 100));
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, m);
    await tester.tap(find.text('Скачать и проверить'));
    await tester.pump();
    await tester.pump();
    expect(find.textContaining('Скачано 25 Б из 100 Б'), findsOneWidget);
    // ядро продолжило и скачало: следующий опрос (раз в секунду) приносит готовый файл
    api.state = UpdateDto(
        current: '0.3.0', platform: 'linux-amd64', available: true, app: '0.4.0', size: 100, download: const UpdateDownloadDto(state: 'failed', total: 100, error: 'файл не прошёл проверку'));
    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(find.textContaining('Скачивание не удалось. Данные обновления не прошли проверку'), findsOneWidget);
    expect(find.text('Повторить'), findsOneWidget);
    m.dispose();
  });

  testWidgets('диалог: нет обновлений и ошибка проверки', (tester) async {
    final api = _UpdApi();
    api.afterCheck = UpdateDto(current: '0.3.0', platform: 'linux-amd64', checked: DateTime.now());
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, m);
    await tester.tap(find.text('Проверить сейчас'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Обновлений нет'), findsOneWidget);
    expect(find.text('Скачать и проверить'), findsNothing);
    api.afterCheck = UpdateDto(current: '0.3.0', platform: 'linux-amd64', checked: DateTime.now(), error: 'публикация 4 старее уже виденной 5 (откат)');
    await tester.tap(find.text('Проверить сейчас'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Проверка не удалась. Сервер отдал список релизов старее'), findsOneWidget);
    expect(find.textContaining('публикация 4 старее'), findsNothing, reason: 'сырой текст спрятан в «Подробностях»');
    await tester.tap(find.text('Подробности'));
    await tester.pumpAndSettle();
    expect(find.textContaining('публикация 4 старее'), findsOneWidget);
    // сетевая ошибка из жизни: DNS и сброс соединения не показываются сырыми
    api.afterCheck = UpdateDto(
        current: '0.3.0',
        platform: 'linux-amd64',
        checked: DateTime.now(),
        error: 'манифест релизов: Get "https://cdn.jsdelivr.net/gh/x/releases.json": dial tcp: lookup cdn.jsdelivr.net: no such host; через активный узел: read tcp 127.0.0.1:41632->127.0.0.1:29080: read: connection reset by peer');
    await tester.tap(find.text('Проверить сейчас'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Проверка не удалась. Сервер обновлений не найден'), findsOneWidget);
    m.dispose();
  });

  testWidgets('плашка на главном экране появляется только при доступном обновлении', (tester) async {
    final api = _UpdApi();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    Widget host() => MaterialApp(home: Scaffold(body: ListenableBuilder(listenable: m, builder: (_, _) => UpdateBanner(model: m))));
    await tester.pumpWidget(host());
    expect(find.textContaining('Доступна версия'), findsNothing);
    m.status = const DaemonStatus(version: '0.3.0-linux', nodes: 1, present: 1, degraded: false, update: UpdateBrief(app: '0.4.0'));
    await tester.pumpWidget(host());
    expect(find.text('Доступна версия 0.4.0'), findsOneWidget);
    m.status = const DaemonStatus(version: '0.3.0-linux', nodes: 1, present: 1, degraded: false, update: UpdateBrief(app: '0.4.0', download: 'ready'));
    await tester.pumpWidget(host());
    expect(find.textContaining('скачана и проверена'), findsOneWidget);
    m.dispose();
  });
}
