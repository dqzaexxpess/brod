import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/platform_paths.dart';

void main() {
  test('Windows: состояние в %LOCALAPPDATA%, сокет рядом, настройки в %APPDATA%, демон — brodd.exe', () {
    final env = {'LOCALAPPDATA': r'C:\Users\u\AppData\Local', 'APPDATA': r'C:\Users\u\AppData\Roaming', 'USERPROFILE': r'C:\Users\u', 'XDG_RUNTIME_DIR': '/игнорируется'};
    expect(stateDirPath(env, windows: true), r'C:\Users\u\AppData\Local\vpnclient');
    expect(socketPathFor(env, windows: true), r'C:\Users\u\AppData\Local\vpnclient\run\brodd.sock');
    expect(appConfigFilePath(env, windows: true), r'C:\Users\u\AppData\Roaming\Brod\app.json');
    final c = broddCandidates(env, exeDir: r'C:\Program Files\Brod', cwd: r'C:\dev', windows: true);
    expect(c.first, r'C:\Program Files\Brod\brodd.exe');
    expect(c.any((p) => p.contains('.local')), false, reason: 'на Windows ~/.local/bin не ищем');
  });

  test('Windows без LOCALAPPDATA/APPDATA: по профилю пользователя; явные переменные сильнее', () {
    const env = {'USERPROFILE': r'C:\Users\u'};
    expect(stateDirPath(env, windows: true), r'C:\Users\u\AppData\Local\vpnclient');
    expect(appConfigFilePath(env, windows: true), r'C:\Users\u\AppData\Roaming\Brod\app.json');
    expect(stateDirPath({'VPNCLIENT_STATE': r'D:\s'}, windows: true), r'D:\s');
    expect(socketPathFor({'VPNCLIENT_SOCK': r'D:\x.sock'}, windows: true), r'D:\x.sock');
    expect(broddCandidates({'BRODD_PATH': r'D:\v.exe'}, exeDir: 'e', cwd: 'c', windows: true).first, r'D:\v.exe');
  });

  test('Windows: служба Brod хранит состояние в %ProgramData%\\Brod, если каталог создан установщиком', () {
    final env = {'ProgramData': r'C:\ProgramData', 'LOCALAPPDATA': r'C:\Users\u\AppData\Local', 'APPDATA': r'C:\Users\u\AppData\Roaming'};
    bool no(String _) => false;
    bool yes(String p) => p == r'C:\ProgramData\Brod';
    expect(stateDirPath(env, windows: true, exists: no), r'C:\Users\u\AppData\Local\vpnclient');
    expect(stateDirPath(env, windows: true, exists: yes), r'C:\ProgramData\Brod');
    expect(socketPathFor(env, windows: true, exists: yes), r'C:\ProgramData\Brod\run\brodd.sock');
    expect(legacyAppConfigFilePath(env), r'C:\Users\u\AppData\Roaming\vpn-client\app.json');
  });

  test('Linux не изменился', () {
    const env = {'HOME': '/home/u', 'XDG_RUNTIME_DIR': '/run/user/1000'};
    expect(socketPathFor(env, windows: false), '/run/user/1000/brod.sock');
    expect(socketPathFor({'HOME': '/home/u'}, windows: false), '/home/u/.local/share/brod/brodd.sock');
    expect(stateDirPath({'HOME': '/home/u', 'XDG_DATA_HOME': '/x'}, windows: false), '/x/brod');
    expect(appConfigFilePath(env, windows: false), '/home/u/.config/brod/app.json');
    expect(legacyAppConfigFilePath(env, windows: false), '/home/u/.config/vpn-client/app.json');
    expect(appConfigFilePath({'HOME': '/h', 'XDG_CONFIG_HOME': '/c'}, windows: false), '/c/brod/app.json');
    expect(stateDirPath({'BROD_STATE': '/n', 'VPNCLIENT_STATE': '/o'}, windows: false), '/n', reason: 'BROD_STATE сильнее прежнего имени');
    expect(stateDirPath({'VPNCLIENT_STATE': '/o'}, windows: false), '/o');
    final c = broddCandidates(env, exeDir: '/opt/brod', cwd: '/dev/app', windows: false);
    expect(c, ['/opt/brod/brodd', '/home/u/.local/bin/brodd'], reason: 'путей относительно текущего каталога в выпуске нет');
    final d = broddCandidates(env, exeDir: '/opt/brod', cwd: '/dev/app', windows: false, dev: true);
    expect(d, ['/opt/brod/brodd', '/home/u/.local/bin/brodd', '/dev/app/../bin/brodd', '/dev/app/bin/brodd']);
  });

  test('сокет единственного экземпляра без XDG_RUNTIME_DIR лежит в каталоге состояния, а не в общем /tmp', () {
    expect(singleInstanceSocketPath({'XDG_RUNTIME_DIR': '/run/user/1000', 'HOME': '/home/u'}), '/run/user/1000/brod-app.sock');
    expect(singleInstanceSocketPath({'HOME': '/home/u'}), '/home/u/.local/share/brod/brod-app.sock');
    expect(singleInstanceSocketPath({'HOME': '/home/u', 'XDG_DATA_HOME': '/x'}), '/x/brod/brod-app.sock');
    expect(singleInstanceSocketPath({'HOME': '/home/u', 'XDG_RUNTIME_DIR': ''}), isNot(startsWith('/tmp')));
  });

  test('признак киллсвитча без XDG_RUNTIME_DIR в каталоге состояния (парно с killswitch.Marker), Windows не меняется', () {
    expect(killswitchMarkerPath({'XDG_RUNTIME_DIR': '/run/user/1000', 'HOME': '/home/u'}, windows: false), '/run/user/1000/brod-killswitch.active');
    expect(killswitchMarkerPath({'HOME': '/home/u'}, windows: false), '/home/u/.local/share/brod/brod-killswitch.active');
    expect(killswitchMarkerPath({'HOME': '/h', 'BROD_STATE': '/s', 'XDG_RUNTIME_DIR': ''}, windows: false), '/s/brod-killswitch.active');
    expect(killswitchMarkerPath({'ProgramData': r'C:\ProgramData'}, windows: true), r'C:\ProgramData\Brod\killswitch.active');
    expect(killswitchMarkerPath({}, windows: true), r'C:\ProgramData\Brod\killswitch.active');
  });

  test('прежний признак в /tmp учитывается при обновлении, но только свой обычный файл', () {
    expect(legacyKillswitchMarkerPath({'HOME': '/h'}, windows: false, tmpDir: '/tmp', uid: '1000'), '/tmp/brod-killswitch-1000.active');
    expect(legacyKillswitchMarkerPath({'XDG_RUNTIME_DIR': '/run/user/1000'}, windows: false, tmpDir: '/tmp', uid: '1000'), isNull);
    expect(legacyKillswitchMarkerPath({'HOME': '/h'}, windows: true, tmpDir: '/tmp', uid: '1000'), isNull);
    if (Platform.isWindows) return;
    final uid = Process.runSync('id', ['-u']).stdout.toString().trim();
    final dir = Directory.systemTemp.createTempSync('fl3');
    addTearDown(() => dir.deleteSync(recursive: true));
    final f = File('${dir.path}/m')..writeAsStringSync('x');
    expect(fileOwnedByUid(f.path, uid), isTrue);
    expect(fileOwnedByUid(f.path, '${int.parse(uid) + 1}'), isFalse, reason: 'чужой владелец');
    Link('${dir.path}/l').createSync(f.path);
    expect(fileOwnedByUid('${dir.path}/l', uid), isFalse, reason: 'ссылка не признак');
    expect(fileOwnedByUid('${dir.path}/нет', uid), isFalse);
  });

  test('домашний каталог для файлов пользователя: на Windows USERPROFILE (HOME там нет), иначе HOME, без них — текущий', () {
    expect(userHomePath({'USERPROFILE': r'C:\Users\u'}, windows: true), r'C:\Users\u');
    expect(userHomePath({'HOME': '/home/u'}, windows: true), '.'); // HOME на Windows не используется
    expect(userHomePath({'HOME': '/home/u'}, windows: false), '/home/u');
    expect(userHomePath({}, windows: false), '.');
  });
}
