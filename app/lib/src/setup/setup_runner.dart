import 'dart:async';
import 'dart:convert';
import 'dart:io';

/// Итог шага установки или удаления.
class SetupOutcome {
  const SetupOutcome(this.exitCode, this.lines);

  final int exitCode;
  final List<String> lines; // содержательные строки протокола (без служебных заголовков PowerShell)

  bool get ok => exitCode == 0;
  bool get cancelled => exitCode == elevationDeclined;

  /// Windows запросила подтверждение прав администратора, пользователь отказал.
  static const elevationDeclined = 1223;
}

/// Выполняет привилегированные шаги. Реализация для тестов подменяется.
abstract class SetupRunner {
  Future<SetupOutcome> install({required String account, required String dir, required void Function(String line) onLine});
  Future<SetupOutcome> uninstall({required String dir, required bool removeData, required void Function(String line) onLine});
}

/// Строки протокола PowerShell (Start-Transcript) без заголовка и подвала сеанса. Заголовок и подвал — блоки между строками из звёздочек;
/// их текст зависит от языка системы, поэтому опознаются по рамке, а не по словам. Служебные строки ошибок PowerShell (`PS>TerminatingError`,
/// положение в скрипте) отбрасываются, повторы подряд схлопываются.
List<String> transcriptLines(String raw) {
  final out = <String>[];
  var inBlock = false;
  for (final l in const LineSplitter().convert(raw)) {
    final t = l.trim();
    if (t.startsWith('*****')) {
      inBlock = !inBlock;
      continue;
    }
    if (inBlock || t.isEmpty) continue;
    if (t.startsWith('PS>') || t.startsWith('+ ') || RegExp(r'^[A-Za-z]:\\.*\.ps1:\d+').hasMatch(t) || RegExp(r'^(CategoryInfo|FullyQualifiedErrorId)\b').hasMatch(t)) continue;
    if (t == 'True' || t == 'False') continue; // случайный вывод логического значения командой скрипта, пользователю он ничего не говорит
    if (out.isNotEmpty && out.last == t) continue;
    out.add(t);
  }
  return out;
}

/// Одна фраза для страницы ошибки: сообщение после «УСТАНОВКА НЕ ЗАВЕРШЕНА:» (его печатает install.ps1), иначе последняя строка протокола.
String failureSummary(List<String> lines) {
  const marker = 'УСТАНОВКА НЕ ЗАВЕРШЕНА:';
  for (var i = lines.length - 1; i >= 0; i--) {
    final j = lines[i].indexOf(marker);
    if (j < 0) continue;
    // консоль PowerShell переносит длинную строку на ширине окна посреди слова: продолжение (оно начинается со строчной буквы) приклеивается
    var t = lines[i].substring(j + marker.length).trim();
    for (var k = i + 1; k < lines.length && RegExp(r'^[а-яa-z(]').hasMatch(lines[k]); k++) {
      t += lines[k];
    }
    return t.isEmpty ? t : t[0].toUpperCase() + t.substring(1);
  }
  return lines.isEmpty ? '' : lines.last;
}

/// Протокол читается как UTF-8 или UTF-16 (в зависимости от метки порядка байтов): кодировку выбирает PowerShell.
String decodeLog(List<int> b) {
  if (b.length >= 2 && b[0] == 0xFF && b[1] == 0xFE) {
    final u = <int>[for (var i = 2; i + 1 < b.length; i += 2) b[i] | (b[i + 1] << 8)];
    return String.fromCharCodes(u);
  }
  final s = b.length >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF ? b.sublist(3) : b;
  return utf8.decode(s, allowMalformed: true);
}

String psQuote(String s) => "'${s.replaceAll("'", "''")}'";

/// Запуск `install.ps1` и `uninstall.ps1` от администратора: окно подтверждения Windows (UAC) показывает сама система, а окно установщика
/// остаётся обычным, поэтому учётная запись по умолчанию — настоящая, а Brod после установки запускается без повышения прав.
class WindowsSetupRunner implements SetupRunner {
  WindowsSetupRunner({required this.scriptsDir, Directory? temp}) : _temp = temp ?? Directory.systemTemp;

  final String scriptsDir; // каталог со скриптами: распакованный архив при установке, временная копия при удалении
  final Directory _temp;

  @override
  Future<SetupOutcome> install({required String account, required String dir, required void Function(String line) onLine}) =>
      _elevated('install.ps1', ['-Dir', dir, '-User', account], onLine);

  @override
  Future<SetupOutcome> uninstall({required String dir, required bool removeData, required void Function(String line) onLine}) =>
      _elevated('uninstall.ps1', ['-Dir', dir, if (removeData) '-RemoveState'], onLine, transcript: false);

  /// Текст небольшого сценария-запускающего: поднимает powershell с правами администратора, ждёт и отдаёт его код возврата.
  static String launcher(String script, List<String> args, String? log) {
    final all = <String>[
      '-NoProfile',
      '-ExecutionPolicy',
      'Bypass',
      '-File',
      '"$script"',
      for (final a in args) '"$a"',
      if (log != null) ...['-Log', '"$log"'],
    ];
    return '''try {
  \$p = Start-Process -FilePath powershell.exe -ArgumentList @(${all.map(psQuote).join(',')}) -Verb RunAs -WindowStyle Hidden -Wait -PassThru
  exit \$p.ExitCode
} catch { exit ${SetupOutcome.elevationDeclined} }
''';
  }

  Future<SetupOutcome> _elevated(String script, List<String> args, void Function(String) onLine, {bool transcript = true}) async {
    final work = await _temp.createTemp('brod-setup-run-');
    final log = transcript ? '${work.path}${Platform.pathSeparator}install.log' : null;
    final launch = File('${work.path}${Platform.pathSeparator}launch.ps1');
    // UTF-8 с меткой: Windows PowerShell 5.1 без неё читает сценарий как ANSI, и кириллица в именах ломается
    await launch.writeAsBytes([0xEF, 0xBB, 0xBF, ...utf8.encode(launcher('$scriptsDir${Platform.pathSeparator}$script', args, log))]);
    final proc = await Process.start('powershell.exe', ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', launch.path], runInShell: false);
    unawaited(proc.stdout.drain<void>());
    unawaited(proc.stderr.drain<void>());
    var shown = 0;
    final timer = Timer.periodic(const Duration(milliseconds: 400), (_) {
      if (log == null) return;
      try {
        final f = File(log);
        if (!f.existsSync()) return;
        final lines = transcriptLines(decodeLog(f.readAsBytesSync()));
        for (; shown < lines.length; shown++) {
          onLine(lines[shown]);
        }
      } on FileSystemException {
        // файл занят записью: прочитаем на следующем шаге
      }
    });
    final code = await proc.exitCode;
    timer.cancel();
    var lines = <String>[];
    if (log != null) {
      try {
        lines = transcriptLines(decodeLog(File(log).readAsBytesSync()));
        for (; shown < lines.length; shown++) {
          onLine(lines[shown]);
        }
      } on FileSystemException {
        // протокола нет: окно покажет только код
      }
    }
    try {
      await work.delete(recursive: true);
    } on FileSystemException {
      // временная папка уберётся сама при очистке системы
    }
    return SetupOutcome(code, lines);
  }
}
