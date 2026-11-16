/// Расположение файлов приложения и демона по платформам. Чистые функции (окружение передаётся аргументом) — проверяются тестами на Linux.
/// Те же правила, что у Go (`paths/paths.go`): иначе приложение и демон искали бы сокет в разных местах.
library;

import 'dart:io';

String _join(String a, String b, {required bool windows}) => '$a${windows ? r'\' : '/'}$b';

/// Первое непустое значение: BROD_* сильнее прежних VPNCLIENT_*.
String _firstEnv(Map<String, String> env, List<String> names) {
  for (final n in names) {
    final v = env[n] ?? '';
    if (v.isNotEmpty) return v;
  }
  return '';
}

String _home(Map<String, String> env, {required bool windows}) => windows ? (env['USERPROFILE'] ?? env['HOMEDRIVE'] ?? '') : (env['HOME'] ?? '');

/// Домашний каталог пользователя для файлов, которые он сохраняет сам (резервная копия): на Windows %USERPROFILE% — переменной HOME там нет,
/// и путь становился относительным, то есть каталогом программы (Program Files: запись запрещена). Пусто — текущий каталог.
String userHomePath(Map<String, String> env, {required bool windows}) {
  final h = _home(env, windows: windows);
  return h.isEmpty ? '.' : h;
}

/// Существует ли каталог (подменяется в тестах).
bool dirExistsOnDisk(String path) => Directory(path).existsSync();

/// Каталог состояния демона: BROD_STATE (прежнее VPNCLIENT_STATE); на Windows %ProgramData%\Brod, если он есть (так живёт служба Brod; создаёт install.ps1),
/// иначе %LOCALAPPDATA%\vpnclient; на остальных платформах $XDG_DATA_HOME/brod или ~/.local/share/brod.
String stateDirPath(Map<String, String> env, {required bool windows, bool Function(String)? exists}) {
  final v = _firstEnv(env, ['BROD_STATE', 'VPNCLIENT_STATE']);
  if (v.isNotEmpty) return v;
  if (windows) {
    final pd = env['ProgramData'] ?? '';
    if (pd.isNotEmpty) {
      final d = _join(pd, 'Brod', windows: true);
      if ((exists ?? dirExistsOnDisk)(d)) return d;
    }
    final la = env['LOCALAPPDATA'] ?? '';
    return la.isNotEmpty ? _join(la, 'vpnclient', windows: true) : _join(_join(_join(_home(env, windows: true), 'AppData', windows: true), 'Local', windows: true), 'vpnclient', windows: true);
  }
  final xdg = env['XDG_DATA_HOME'] ?? '';
  return xdg.isNotEmpty ? '$xdg/brod' : '${_home(env, windows: false)}/.local/share/brod';
}

/// Сокет демона: BROD_SOCK (прежнее VPNCLIENT_SOCK); на Linux $XDG_RUNTIME_DIR/brod.sock; иначе brodd.sock в каталоге состояния; на Windows — run\\brodd.sock в каталоге состояния.
String socketPathFor(Map<String, String> env, {required bool windows, bool Function(String)? exists}) {
  final v = _firstEnv(env, ['BROD_SOCK', 'VPNCLIENT_SOCK']);
  if (v.isNotEmpty) return v;
  if (!windows) {
    final rt = env['XDG_RUNTIME_DIR'] ?? '';
    if (rt.isNotEmpty) return '$rt/brod.sock';
  }
  final state = stateDirPath(env, windows: windows, exists: exists);
  // Windows: сам каталог состояния закрыт для пользователя, ему открыт только подкаталог run (его создаёт install.ps1)
  return windows ? _join(_join(state, 'run', windows: true), 'brodd.sock', windows: true) : _join(state, 'brodd.sock', windows: windows);
}

/// Сокет единственного экземпляра приложения (Linux): $XDG_RUNTIME_DIR/brod-app.sock, без него — в каталоге состояния пользователя,
/// а не в общем /tmp, где чужой пользователь мог бы занять имя заранее.
String singleInstanceSocketPath(Map<String, String> env) {
  final rt = env['XDG_RUNTIME_DIR'] ?? '';
  return '${rt.isNotEmpty ? rt : stateDirPath(env, windows: false)}/brod-app.sock';
}

/// Файл-признак «правила киллсвитча стоят» (его пишет демон, `killswitch.Marker`): Windows — %ProgramData%\Brod\killswitch.active
/// (демон — служба), иначе $XDG_RUNTIME_DIR/brod-killswitch.active, без него — каталог состояния пользователя, а не общий /tmp.
String killswitchMarkerPath(Map<String, String> env, {required bool windows}) {
  if (windows) return _join(_join(env['ProgramData'] ?? r'C:\ProgramData', 'Brod', windows: true), 'killswitch.active', windows: true);
  final rt = env['XDG_RUNTIME_DIR'] ?? '';
  return '${rt.isNotEmpty ? rt : stateDirPath(env, windows: false)}/brod-killswitch.active';
}

/// Прежнее расположение признака без XDG_RUNTIME_DIR: `<tmp>/brod-killswitch-<uid>.active`. Null там, где его не было
/// (Windows, есть XDG_RUNTIME_DIR). Нужен на время обновления: правила прежнего демона могут остаться с признаком в /tmp.
String? legacyKillswitchMarkerPath(Map<String, String> env, {required bool windows, required String tmpDir, required String uid}) {
  if (windows || (env['XDG_RUNTIME_DIR'] ?? '').isNotEmpty) return null;
  return '$tmpDir/brod-killswitch-$uid.active';
}

/// Обычный файл (не ссылка), владелец — пользователь `uid` (Linux/macOS: `stat -c %u`).
bool fileOwnedByUid(String path, String uid) {
  try {
    if (FileSystemEntity.typeSync(path, followLinks: false) != FileSystemEntityType.file) return false;
    return Process.runSync('stat', ['-c', '%u', path]).stdout.toString().trim() == uid;
  } catch (_) {
    return false;
  }
}

/// Файл настроек приложения: на Windows %APPDATA%\Brod\app.json, иначе $XDG_CONFIG_HOME/brod/app.json или ~/.config/brod/app.json
/// (прежний каталог vpn-client переносит Prefs.defaultFile копированием).
String appConfigFilePath(Map<String, String> env, {required bool windows}) {
  if (windows) {
    final ad = env['APPDATA'] ?? '';
    final base = ad.isNotEmpty ? ad : _join(_join(_home(env, windows: true), 'AppData', windows: true), 'Roaming', windows: true);
    return _join(_join(base, 'Brod', windows: true), 'app.json', windows: true);
  }
  final xdg = env['XDG_CONFIG_HOME'] ?? '';
  final base = xdg.isNotEmpty ? xdg : '${_home(env, windows: false)}/.config';
  return '$base/brod/app.json';
}

/// Прежний файл настроек приложения (до переименования в Brod): %APPDATA%\vpn-client\app.json на Windows, ~/.config/vpn-client/app.json на Linux.
String legacyAppConfigFilePath(Map<String, String> env, {bool windows = true}) {
  if (!windows) {
    final xdg = env['XDG_CONFIG_HOME'] ?? '';
    final base = xdg.isNotEmpty ? xdg : '${_home(env, windows: false)}/.config';
    return '$base/vpn-client/app.json';
  }
  final ad = env['APPDATA'] ?? '';
  final base = ad.isNotEmpty ? ad : _join(_join(_home(env, windows: true), 'AppData', windows: true), 'Roaming', windows: true);
  return _join(_join(base, 'vpn-client', windows: true), 'app.json', windows: true);
}

/// Где искать демон: BRODD_PATH, рядом с приложением, (Linux) ~/.local/bin, каталог разработки (только при dev). На Windows имя файла — brodd.exe.
List<String> broddCandidates(Map<String, String> env, {required String exeDir, required String cwd, required bool windows, bool dev = false}) {
  final name = windows ? 'brodd.exe' : 'brodd';
  final sep = windows ? r'\' : '/';
  return [
    if ((env['BRODD_PATH'] ?? '').isNotEmpty) env['BRODD_PATH']!,
    '$exeDir$sep$name',
    if (!windows && _home(env, windows: false).isNotEmpty) '${_home(env, windows: false)}/.local/bin/brodd',
    // пути разработки: только в отладочной сборке, иначе запустился бы чужой bin/brodd из текущего каталога
    if (dev) '$cwd$sep..${sep}bin$sep$name',
    if (dev) '$cwd${sep}bin$sep$name',
  ];
}
