import 'dart:async';
import 'dart:io';

import 'package:flutter_local_notifications_windows/flutter_local_notifications_windows.dart';

/// Операция уведомления от демона (`GET /v1/notifications`): show — показать или заменить, close — убрать.
class NoticeOp {
  const NoticeOp({required this.op, required this.id, this.summary = '', this.body = '', this.actions = const [], this.timeoutMs = 0});

  final String op;
  final int id;
  final String summary;
  final String body;
  final List<(String key, String label)> actions;
  final int timeoutMs; // 0 — по умолчанию системы; <0 — не скрывать

  factory NoticeOp.fromJson(Map<String, dynamic> j) => NoticeOp(
        op: (j['op'] as String?) ?? '',
        id: (j['id'] as num?)?.toInt() ?? 0,
        summary: (j['summary'] as String?) ?? '',
        body: (j['body'] as String?) ?? '',
        actions: [
          for (final a in (j['actions'] as List?) ?? const [])
            ((a as Map<String, dynamic>)['key'] as String? ?? '', a['label'] as String? ?? ''),
        ],
        timeoutMs: (j['timeout_ms'] as num?)?.toInt() ?? 0,
      );
}

/// Ответ долгого опроса.
class NoticeBatch {
  const NoticeBatch(this.next, this.ops);

  final int next;
  final List<NoticeOp> ops;

  factory NoticeBatch.fromJson(Map<String, dynamic> j) => NoticeBatch(
        (j['next'] as num?)?.toInt() ?? 0,
        [for (final o in (j['ops'] as List?) ?? const []) NoticeOp.fromJson(o as Map<String, dynamic>)],
      );
}

/// Строка, которую Windows возвращает при нажатии: «id|ключ кнопки» (пустой ключ — нажатие на само уведомление).
String noticeArgs(int id, String key) => '$id|$key';

/// Разбирает [noticeArgs]; null — не наша строка.
(int, String)? parseNoticeArgs(String? s) {
  if (s == null) return null;
  final i = s.indexOf('|');
  if (i <= 0) return null;
  final id = int.tryParse(s.substring(0, i));
  return id == null ? null : (id, s.substring(i + 1));
}

/// Показывает уведомления демона системными уведомлениями Windows. Демон работает службой в сеансе 0 и сам их показать
/// не может: он отдаёт их долгим опросом, а нажатия кнопок принимает обратно. Что и когда показывать, решает демон.
class WinNotices {
  WinNotices({required this.poll, required this.action, required this.onOpen});

  final Future<NoticeBatch> Function(int after) poll;
  final Future<void> Function(int id, String key) action;
  final void Function() onOpen; // нажатие на само уведомление — показать окно

  final _plugin = FlutterLocalNotificationsWindows();
  bool _running = false;

  /// Журнал уведомлений приложения (%APPDATA%\Brod\notices.log, до 64 КБ): что пришло от демона, что вернула Windows при
  /// нажатии и чем ответил демон — без него не понять, на каком шаге потерялось нажатие.
  static final _logFile = File('${Platform.environment['APPDATA'] ?? '.'}\\Brod\\notices.log');

  static void _log(String s) {
    try {
      if (_logFile.existsSync() && _logFile.lengthSync() > 64 * 1024) _logFile.writeAsStringSync('');
      _logFile.parent.createSync(recursive: true);
      _logFile.writeAsStringSync('${DateTime.now().toIso8601String()} $s\n', mode: FileMode.append);
    } catch (_) {}
  }

  Future<void> start() async {
    if (!Platform.isWindows || _running) return;
    _running = true;
    final ok = await _plugin.initialize(
      settings: const WindowsInitializationSettings(appName: 'Brod', appUserModelId: 'Brod.VPN', guid: '5f0c8a3e-4b7d-4e2a-9c61-2d3b8e7f1a90'),
      onDidReceiveNotificationResponse: (r) {
        _log('ответ Windows: type=${r.notificationResponseType} actionId=${r.actionId} payload=${r.payload}');
        _onResponse(r.actionId ?? r.payload);
      },
    );
    _log('инициализация: $ok');
    unawaited(_loop());
  }

  void stop() => _running = false;

  void _onResponse(String? args) {
    final p = parseNoticeArgs(args);
    if (p == null) {
      _log('нажатие: не разобрать «$args»');
      return;
    }
    final (id, key) = p;
    if (key.isEmpty) {
      onOpen();
    } else {
      // уведомление могло устареть (демон перезапускался) — не страшно, но в журнал
      action(id, key).then((_) => _log('нажатие $id/$key: демон принял'), onError: (Object e) => _log('нажатие $id/$key: $e'));
    }
  }

  Future<void> _loop() async {
    var after = 0;
    while (_running) {
      try {
        final b = await poll(after);
        if (b.next < after) {
          // демон перезапустился: его прежние уведомления больше не действуют
          await _plugin.cancelAll();
        }
        for (final op in b.ops) {
          await _apply(op);
        }
        after = b.next;
      } catch (e) {
        _log('опрос: $e');
        await Future<void>.delayed(const Duration(seconds: 3)); // демон недоступен или перезапускается
      }
    }
  }

  Future<void> _apply(NoticeOp op) async {
    _log('${op.op} ${op.id}: ${op.summary} [${op.actions.map((a) => a.$1).join(',')}]');
    if (op.op == 'close') {
      await _plugin.cancel(id: op.id);
      return;
    }
    if (op.op != 'show') return;
    await _plugin.show(
      id: op.id,
      title: op.summary,
      body: op.body,
      payload: noticeArgs(op.id, ''),
      notificationDetails: WindowsNotificationDetails(
        actions: [for (final (key, label) in op.actions) WindowsAction(content: label, arguments: noticeArgs(op.id, key))],
        duration: op.timeoutMs < 0 ? WindowsNotificationDuration.long : null,
      ),
    );
  }
}
