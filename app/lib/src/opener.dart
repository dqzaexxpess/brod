import 'dart:io';

import 'android_bridge.dart';

/// Открывает ссылку системным обработчиком (для tg://… это Telegram). Возвращает сообщение об ошибке или null.
/// Android — отдельный путь (намерение), появится вместе со сборкой под Android.
typedef LinkOpener = Future<String?> Function(String url);

/// [run] — запуск внешней команды (по умолчанию Process.run); подменяется только в тестах.
Future<String?> openLink(String url, {Future<ProcessResult> Function(String, List<String>)? run}) async {
  run ??= Process.run;
  if (Platform.isAndroid) {
    final ok = await AndroidCore.openUrl(url);
    if (ok) return null;
    return url.startsWith('tg://') ? 'Telegram не найден. Ссылку можно скопировать из меню.' : 'Нет приложения, способного открыть эту ссылку';
  }
  try {
    final ProcessResult r;
    if (Platform.isLinux) {
      r = await run('xdg-open', [url]);
    } else if (Platform.isWindows) {
      // не `cmd /c start`: cmd режет ссылку по «&» (tg://proxy?server=…&port=…&secret=…), а rundll32 передаёт её обработчику целиком
      r = await run('rundll32', ['url.dll,FileProtocolHandler', url]);
    } else if (Platform.isMacOS) {
      r = await run('open', [url]);
    } else {
      return 'Открытие ссылок на этой платформе пока не реализовано';
    }
    if (r.exitCode != 0) return 'Не удалось открыть ссылку. Telegram установлен?';
    return null;
  } on ProcessException catch (e) {
    return 'Не удалось открыть ссылку: ${e.message}';
  }
}
