import 'dart:io';

/// Запуск приложения при входе в систему (трей и уведомления). Открывать ли при этом окно, решает общая настройка
/// «Запускать свёрнутым в трей» (Prefs.startHidden) — та же, что для ручного запуска.
///
/// Windows — значение в автозагрузке пользователя (`HKCU\...\Run`, без прав администратора; демон — служба и стартует сам).
/// Linux — `~/.config/autostart/brod.desktop` (XDG). На других системах недоступно.
class Autostart {
  static const _runKey = r'HKCU\Software\Microsoft\Windows\CurrentVersion\Run';
  static const _valueName = 'Brod';

  static bool get supported => Platform.isWindows || Platform.isLinux;

  static String get _exe => Platform.resolvedExecutable;

  static File get _desktopFile {
    final env = Platform.environment;
    final base = (env['XDG_CONFIG_HOME'] ?? '').isNotEmpty ? env['XDG_CONFIG_HOME']! : '${env['HOME'] ?? '.'}/.config';
    return File('$base/autostart/brod.desktop');
  }

  /// Включён ли автозапуск (и указывает ли он на это приложение).
  static Future<bool> enabled() async {
    if (Platform.isWindows) {
      final r = await Process.run('reg', ['query', _runKey, '/v', _valueName]);
      return r.exitCode == 0 && r.stdout.toString().toLowerCase().contains(_exe.toLowerCase());
    }
    if (Platform.isLinux) return _desktopFile.existsSync();
    return false;
  }

  /// Включает или выключает автозапуск. Возвращает текст ошибки или null.
  static Future<String?> set(bool on) async {
    try {
      if (Platform.isWindows) {
        final args = on
            ? ['add', _runKey, '/v', _valueName, '/t', 'REG_SZ', '/d', '"$_exe"', '/f']
            : ['delete', _runKey, '/v', _valueName, '/f'];
        final r = await Process.run('reg', args);
        // удалить отсутствующее значение — не ошибка
        if (r.exitCode != 0 && on) return 'Не удалось записать автозагрузку: ${r.stderr.toString().trim()}';
        return null;
      }
      if (Platform.isLinux) {
        final f = _desktopFile;
        if (!on) {
          if (f.existsSync()) f.deleteSync();
          return null;
        }
        f.parent.createSync(recursive: true);
        f.writeAsStringSync('[Desktop Entry]\n'
            'Type=Application\n'
            'Name=Brod\n'
            'Comment=Brod VPN\n'
            'Exec="$_exe"\n'
            'Icon=dev.godbtw.brod\n'
            'Terminal=false\n'
            'X-GNOME-Autostart-enabled=true\n');
        return null;
      }
      return 'Автозапуск на этой системе не поддерживается';
    } catch (e) {
      return 'Не удалось изменить автозапуск: $e';
    }
  }
}
