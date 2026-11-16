import 'dart:io';

/// Пишет файл, который не должен быть читаем другим пользователям (резервная копия с паролями серверов):
/// сначала создаёт пустой файл и закрывает права, и только потом пишет содержимое, поэтому секреты не лежат под обычной маской.
/// Linux/macOS: chmod 600. Windows: icacls, доступ только текущему пользователю (без наследования от каталога).
/// [beforeWrite] вызывается между закрытием прав и записью (для теста).
Future<void> writePrivateFile(String path, String text, {void Function(String path)? beforeWrite}) async {
  final f = File(path);
  await f.writeAsString('');
  await _restrict(path);
  beforeWrite?.call(path);
  await f.writeAsString(text);
}

Future<void> _restrict(String path) async {
  if (Platform.isWindows) {
    final user = Platform.environment['USERNAME'] ?? '';
    if (user.isEmpty) return;
    try {
      await Process.run('icacls', [path, '/inheritance:r', '/grant:r', '$user:(R,W,D)']);
    } catch (_) {}
    return;
  }
  try {
    await Process.run('chmod', ['600', path]);
  } catch (_) {}
}
