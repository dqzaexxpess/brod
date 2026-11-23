import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';

import 'android_bridge.dart';
import 'api.dart';
import 'model.dart';
import 'prefs.dart';
import 'ui.dart';

/// Запуск на Android: без трея, без единственного экземпляра и без запуска демона — ядро уже внутри процесса.
Future<void> runAndroid() async {
  final String sock;
  try {
    sock = await AndroidCore.start();
  } catch (e) {
    runApp(MaterialApp(home: Scaffold(body: Center(child: Padding(padding: const EdgeInsets.all(24), child: Text('Ядро не запустилось: $e'))))));
    return;
  }
  final api = UnixApi(sock);
  final model = AppModel(api);
  model.beforeVpnOn = () async {
    if (!await AndroidCore.prepareVpn()) throw ApiException(0, 'Нужно разрешение на VPN: без него Android не даст создать туннель');
  };
  final prefs = Prefs.load(File('${File(sock).parent.path}/app.json'));
  WidgetsBinding.instance.addObserver(_Foreground(model, api, prefs));
  unawaited(api.setUiVisible(true)); // запуск из значка: интерфейс уже на экране, смены состояния не будет
  runApp(VpnApp(model: model, prefs: prefs));
  model.start();
  // ссылки на узлы из других приложений (vless:// и т. п.): та, с которой открыли, и те, что придут, пока приложение открыто
  AndroidCore.listenLinks(model.offerImport);
  unawaited(AndroidCore.takeLink().then((l) {
    if (l != null) model.offerImport(l);
  }));
}

/// Следит за жизненным циклом: в фоне опрос ядра замедляется (экран погашен или приложение свёрнуто).
class _Foreground with WidgetsBindingObserver {
  _Foreground(this.model, this.api, this.prefs);
  final AppModel model;
  final UnixApi api;
  final Prefs prefs;
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    final visible = state == AppLifecycleState.resumed;
    model.setForeground(visible);
    unawaited(api.setUiVisible(visible));
    // скопированная ссылка на узел (из Telegram и т. п.): предлагаем добавить; чтение буфера Android разрешает только окну в фокусе, поэтому с небольшой задержкой
    if (visible && prefs.clipboardImport) Future<void>.delayed(const Duration(milliseconds: 400), model.checkClipboard);
  }
}
