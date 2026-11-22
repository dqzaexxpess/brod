import 'dart:io';

/// Журнал окна установки: тот же файл, в который пишет загрузчик setup.exe (путь он передаёт в BROD_SETUP_LOG). По нему видно, на каком шаге
/// окно закрылось, если оно закрылось без сообщения.
String setupLogPath() => Platform.environment['BROD_SETUP_LOG'] ?? '${Directory.systemTemp.path}${Platform.pathSeparator}brod-setup.log';

/// Номер запуска, выданный загрузчиком: по строке `UI-EXIT run=…` он отличает намеренное закрытие окна от падения.
String setupRunId() => Platform.environment['BROD_SETUP_RUN'] ?? '';

void setupLog(String line) {
  try {
    File(setupLogPath()).writeAsStringSync('${DateTime.now().toIso8601String()} $line\n', mode: FileMode.append, flush: true);
  } catch (_) {
    // журнал необязателен: ошибка записи установке не мешает
  }
}

/// Намеренный выход окна: пишется до exit, чтобы загрузчик понял, что это не падение.
void setupExit(int code) {
  setupLog('UI-EXIT run=${setupRunId()} code=$code');
  exit(code);
}
