import 'dart:io';

/// Демона нет: файла сокета нет (ENOENT) или сокет остался от прошлого запуска, а слушателя нет (ECONNREFUSED: 111 в Linux, 10061 в Windows).
/// Это обычное состояние «демон не запущен», а не загадочная ошибка.
bool daemonNotRunning(Object e) {
  if (e is! SocketException) return false;
  final code = e.osError?.errorCode;
  return code == 2 || code == 111 || code == 61 || code == 10061 || code == 10053 || code == 10054;
}

/// Демон работает, но у пользователя нет доступа к его сокету или каталогу состояния: EACCES (13), в Windows ERROR_ACCESS_DENIED (5) и WSAEACCES (10013).
/// Так бывает, когда Brod установлен для другого пользователя: служба запущена, а приложение пишет «Демон не запущен».
bool daemonAccessDenied(Object e) {
  if (e is! SocketException && e is! FileSystemException) return false;
  final code = e is SocketException ? e.osError?.errorCode : (e as FileSystemException).osError?.errorCode;
  return code == 13 || code == 5 || code == 10013;
}

/// Последние [n] непустых строк текста (для показа хвоста журнала демона).
String tailLines(String text, [int n = 8]) {
  final lines = text.split('\n').map((l) => l.trimRight()).where((l) => l.isNotEmpty).toList();
  return lines.skip(lines.length > n ? lines.length - n : 0).join('\n');
}

/// Текст для экрана «демон недоступен»: понятная причина и хвост журнала демона, если он есть.
String daemonDownText(Object e, {String? logTail, bool? windows}) {
  if (daemonAccessDenied(e)) {
    // служба работает, но этому пользователю нет доступа: на Windows лечится установкой для его учётной записи, иначе — правами каталога
    return (windows ?? Platform.isWindows)
        ? 'Нет доступа к демону: Brod установлен для другого пользователя.\nПереустановите Brod для этой учётной записи.'
        : 'Нет доступа к сокету демона: проверьте права на каталог состояния.';
  }
  final head = daemonNotRunning(e) ? 'Демон не запущен.' : 'Демон не отвечает: $e';
  if (logTail == null || logTail.isEmpty) return head;
  return '$head\n\nЖурнал демона (brodd.log):\n$logTail';
}
