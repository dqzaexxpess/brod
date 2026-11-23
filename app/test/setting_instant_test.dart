import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';

class _Api implements DaemonApi {
  final gate = Completer<void>();
  bool failPut = false;
  Map<String, String> server = {'degrade.action': 'notify'};

  @override
  Future<Map<String, String>> settings() async => Map.of(server);

  @override
  Future<List<SourceDto>> sources() async => const [];

  @override
  Future<void> putSetting(String key, String value) async {
    await gate.future; // запись «долгая»: интерфейс не должен её ждать
    if (failPut) throw ApiException(500, 'не записалось');
    server[key] = value;
  }

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  test('настройка меняется в интерфейсе сразу, не дожидаясь записи и перезагрузки', () async {
    final api = _Api();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await m.loadSettings();
    expect(m.settings['degrade.action'], 'notify');
    final done = m.setSetting('degrade.action', 'auto');
    expect(m.settings['degrade.action'], 'auto', reason: 'новое значение видно до ответа демона');
    api.gate.complete();
    await done;
    expect(m.settings['degrade.action'], 'auto');
    m.dispose();
  });

  test('при ошибке записи прежнее значение возвращается и показана причина', () async {
    final api = _Api()..failPut = true;
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await m.loadSettings();
    final done = m.setSetting('degrade.action', 'auto');
    expect(m.settings['degrade.action'], 'auto');
    api.gate.complete();
    await done;
    expect(m.settings['degrade.action'], 'notify', reason: 'демон ничего не записал: показываем настоящее значение');
    expect(m.toast, contains('не записалось'));
    m.dispose();
  });

  test('настройки показываются, не дожидаясь источников', () async {
    final gate = Completer<List<SourceDto>>();
    final m = AppModel(_SlowSources(gate), pollEvery: const Duration(hours: 1));
    final load = m.loadSettings();
    await Future<void>.delayed(const Duration(milliseconds: 20));
    expect(m.settings['degrade.action'], 'notify', reason: 'настройки уже на экране, источники ещё грузятся');
    expect(m.settingsLoading, false, reason: 'полоса загрузки не ждёт источники');
    gate.complete(const []);
    await load;
    m.dispose();
  });
}

class _SlowSources extends _Api {
  _SlowSources(this.g);
  final Completer<List<SourceDto>> g;
  @override
  Future<List<SourceDto>> sources() => g.future;
}
