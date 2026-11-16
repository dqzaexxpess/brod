import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/private_file.dart';

void main() {
  test('права закрыты до записи содержимого, даже если файл уже был с обычной маской', () async {
    final dir = Directory.systemTemp.createTempSync('private');
    addTearDown(() => dir.deleteSync(recursive: true));
    final path = '${dir.path}/b.json';
    File(path).writeAsStringSync('старое');
    await Process.run('chmod', ['644', path]);
    int? modeAtWrite;
    String? contentAtWrite;
    await writePrivateFile(path, 'секрет', beforeWrite: (p) {
      modeAtWrite = File(p).statSync().mode & 0x1ff;
      contentAtWrite = File(p).readAsStringSync();
    });
    expect(modeAtWrite, 384, reason: '0600 до записи');
    expect(contentAtWrite, '', reason: 'старое содержимое стёрто до записи нового');
    expect(File(path).readAsStringSync(), 'секрет');
    expect(File(path).statSync().mode & 0x1ff, 384);
  }, skip: Platform.isWindows);
}
