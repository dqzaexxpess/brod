import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/single.dart';

void main() {
  test('второй экземпляр не запускается, а просит первый показать окно', () async {
    final dir = Directory.systemTemp.createTempSync('single');
    final path = '${dir.path}/app.sock';
    var shown = 0;
    final first = await SingleInstance.acquire(() => shown++, path);
    expect(first, isNotNull);
    final second = await SingleInstance.acquire(() => fail('второй не должен получить сокет'), path);
    expect(second, isNull);
    await Future<void>.delayed(const Duration(milliseconds: 200));
    expect(shown, 1);
    await first!.close();
    // после закрытия первого сокет свободен; устаревший файл не мешает
    File(path).writeAsStringSync('старый файл');
    final third = await SingleInstance.acquire(() {}, path);
    expect(third, isNotNull);
    await third!.close();
    dir.deleteSync(recursive: true);
  });
  raceTest();
}

// Два экземпляра запущены одновременно (автозапуск и восстановление сеанса): блокировку держит другой процесс, сокета у него ещё нет.
// Второй не становится «первым», а ждёт сокет и передаёт «показать». Блокировку держит python3 (fcntl, как и у Dart).
void raceTest() {
  test('гонка двух запусков: владелец блокировки ещё без сокета, второй ждёт и просит показать', () async {
    final py = Process.runSync('which', ['python3']);
    if (py.exitCode != 0) return; // нет python3: проверка пропущена
    final dir = Directory.systemTemp.createTempSync('single-race');
    final path = '${dir.path}/app.sock';
    final holder = await Process.start('python3', [
      '-c',
      'import fcntl,sys,time,socket,os\n'
          'f=open(sys.argv[1]+".lock","w"); fcntl.lockf(f, fcntl.LOCK_EX)\n'
          'print("locked", flush=True)\n'
          'time.sleep(0.6)\n' // «ещё поднимается»
          's=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(1)\n'
          'c,_=s.accept(); data=c.recv(16); print("got", data.decode().strip(), flush=True)\n',
      path,
    ]);
    final lines = holder.stdout.transform(const SystemEncoding().decoder).asBroadcastStream();
    await lines.firstWhere((l) => l.contains('locked'));
    final second = await SingleInstance.acquire(() => fail('второй не должен стать первым'), path);
    expect(second, isNull);
    expect(await lines.join().timeout(const Duration(seconds: 5)), contains('got show'));
    await holder.exitCode;
    dir.deleteSync(recursive: true);
  });
}
