import 'dart:convert';

/// Запасной доступ к демону по loopback-HTTP с токеном (файл `api-access.json` в каталоге состояния; его пишет brodd на Windows).
class ApiAccess {
  const ApiAccess(this.base, this.token);

  final String base; // http://127.0.0.1:29081
  final String token;
}

ApiAccess? parseApiAccess(String text) {
  try {
    final j = jsonDecode(text);
    if (j is! Map) return null;
    final addr = j['addr'], token = j['token'];
    if (addr is! String || token is! String || addr.isEmpty || token.isEmpty) return null;
    return ApiAccess('http://$addr', token);
  } catch (_) {
    return null;
  }
}

/// Сокет есть, но подключиться к нему нельзя из-за прав: на Windows приложение со средним уровнем целостности и демон с высоким
/// (ERROR_ACCESS_DENIED = 5, WSAEACCES = 10013). Обычное «демона нет» (нет файла, отказ в соединении) сюда не относится.
bool socketAccessDenied(int? osErrorCode, {required bool windows}) => windows && (osErrorCode == 5 || osErrorCode == 10013);
