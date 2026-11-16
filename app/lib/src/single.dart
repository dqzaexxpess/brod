import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'platform_paths.dart';

/// Один экземпляр приложения: первый запуск слушает unix-сокет, повторный отправляет ему «показать окно» и завершается.
class SingleInstance {
  SingleInstance._(this._server, this.path, [this._lock]);

  final ServerSocket? _server;
  final RandomAccessFile? _lock;
  final String path;

  static String defaultPath() {
    return singleInstanceSocketPath(Platform.environment);
  }

  /// Пробует стать единственным экземпляром. Возвращает null, если уже запущен другой (ему отправлено «показать»).
  ///
  /// Два экземпляра, запущенные одновременно (автозапуск и восстановление сеанса X11 при входе), раньше оба не находили слушателя и оба
  /// становились «первыми»: открывались две копии, и вторая сообщала, что каталог состояния занят. Теперь владельца определяет блокировка
  /// файла (отпускается сама, когда процесс умер, в том числе при аварийном выключении), а сокет нужен только для сообщения «показать».
  static Future<SingleInstance?> acquire(void Function() onShow, [String? socketPath]) async {
    final path = socketPath ?? defaultPath();
    if (socketPath == null) _ensurePrivateDir(File(path).parent);
    final addr = InternetAddress(path, type: InternetAddressType.unix);
    if (await _askToShow(addr)) return null; // уже есть работающий экземпляр
    RandomAccessFile? lock;
    try {
      lock = File('$path.lock').openSync(mode: FileMode.write);
      lock.lockSync(FileLock.exclusive);
    } on FileSystemException {
      // блокировка у другого экземпляра, который ещё не открыл сокет (идёт запуск): дождаться сокета и передать «показать»
      try {
        lock?.closeSync();
      } catch (_) {}
      for (var i = 0; i < 40; i++) {
        await Future<void>.delayed(const Duration(milliseconds: 100));
        if (await _askToShow(addr)) return null;
      }
      return null; // хозяин блокировки жив, но сокета нет: вторую копию всё равно не запускаем
    }
    try {
      File(path).deleteSync();
    } catch (_) {}
    final server = await ServerSocket.bind(addr, 0);
    server.listen((c) {
      c.cast<List<int>>().transform(utf8.decoder).listen((t) {
        if (t.contains('show')) onShow();
      }, onError: (_) {}, cancelOnError: true);
    });
    return SingleInstance._(server, path, lock);
  }

  /// Каталог для сокета (запасной вариант без XDG_RUNTIME_DIR) создаётся и закрывается правами 0700.
  static void _ensurePrivateDir(Directory d) {
    try {
      if (d.existsSync()) return;
      d.createSync(recursive: true);
      Process.runSync('chmod', ['700', d.path]);
    } catch (_) {}
  }

  static Future<bool> _askToShow(InternetAddress addr) async {
    try {
      final s = await Socket.connect(addr, 0).timeout(const Duration(seconds: 2));
      s.write('show\n');
      await s.flush();
      await s.close();
      return true;
    } catch (_) {
      return false; // никто не слушает (файл мог остаться от упавшего экземпляра)
    }
  }

  Future<void> close() async {
    await _server?.close();
    try {
      File(path).deleteSync();
    } catch (_) {}
    try {
      _lock?.unlockSync();
      _lock?.closeSync();
    } catch (_) {}
  }
}
