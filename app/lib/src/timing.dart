import 'dart:io';

import 'platform_paths.dart';

/// Журнал длительностей действий пользователя (включить, выключить, сменить режим, выйти): жалоба «долго» должна опираться на разбивку по этапам,
/// а не на впечатление. Строки идут в консоль (на Android это logcat, тег flutter) и, где есть каталог состояния, в `app-timing.log` рядом с журналом демона
/// (при размере больше 256 КБ файл обрезается до последней половины). Ошибки записи игнорируются.
void logTiming(String what, Map<String, Duration> stages) {
  final total = stages.values.fold(Duration.zero, (a, b) => a + b);
  final parts = stages.entries.map((e) => '${e.key} ${e.value.inMilliseconds} мс').join(', ');
  final line = '${DateTime.now().toIso8601String()} $what: $parts, всего ${total.inMilliseconds} мс';
  // в тестах (flutter test) ничего не пишем: иначе тесты засоряют рабочий журнал
  if (Platform.environment.containsKey('FLUTTER_TEST')) return;
  // ignore: avoid_print
  print('timing: $line');
  if (Platform.isAndroid) return;
  try {
    final dir = stateDirPath(Platform.environment, windows: Platform.isWindows);
    final f = File('$dir${Platform.pathSeparator}app-timing.log');
    if (!f.parent.existsSync()) return;
    if (f.existsSync() && f.lengthSync() > 256 * 1024) {
      final text = f.readAsStringSync();
      f.writeAsStringSync(text.substring(text.length ~/ 2));
    }
    f.writeAsStringSync('$line\n', mode: FileMode.append);
  } catch (_) {}
}
