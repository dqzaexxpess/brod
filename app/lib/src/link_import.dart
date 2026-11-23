/// Схемы ссылок на узлы, которые Brod принимает из внешних приложений и из буфера обмена (те же, что в AndroidManifest).
const importSchemes = ['vless', 'vmess', 'trojan', 'ss', 'hysteria2', 'hy2', 'tuic', 'wireguard'];

/// Текст — одна ссылка на узел: одна строка, схема из [importSchemes], после схемы что-то есть.
bool looksLikeNodeLink(String? text) {
  if (text == null) return false;
  final t = text.trim();
  if (t.isEmpty || t.length > 4000 || t.contains('\n') || t.contains(' ')) return false;
  final i = t.indexOf('://');
  if (i <= 0 || i + 3 >= t.length) return false;
  return importSchemes.contains(t.substring(0, i).toLowerCase());
}

/// Что показать в вопросе «добавить?»: протокол и адрес сервера без секретов (UUID, пароль, ключи не показываются).
String nodeLinkSummary(String link) {
  final t = link.trim();
  final i = t.indexOf('://');
  final scheme = i > 0 ? t.substring(0, i).toLowerCase() : 'узел';
  // vmess:// — base64 с JSON: адрес не разобрать без декодирования, сервер не показываем
  if (scheme == 'vmess') return 'vmess';
  final rest = i > 0 ? t.substring(i + 3) : t;
  final at = rest.lastIndexOf('@');
  var host = at >= 0 ? rest.substring(at + 1) : rest;
  for (final cut in ['?', '#', '/']) {
    final k = host.indexOf(cut);
    if (k >= 0) host = host.substring(0, k);
  }
  if (host.isEmpty || host.length > 80) return scheme;
  return '$scheme · $host';
}

/// Префикс ссылки на профиль правил (так же, как в ядре: core.shareLinkPrefix).
const profileLinkPrefix = 'brod://route/';

/// Текст — ссылка на профиль правил маршрутизации brod://route/…
bool looksLikeProfileLink(String? text) {
  if (text == null) return false;
  final t = text.trim();
  return t.length > profileLinkPrefix.length && t.length <= 20000 && !t.contains(' ') && !t.contains('\n') && t.startsWith(profileLinkPrefix);
}

/// Ссылка, которую Brod умеет принять: узел или профиль правил.
bool looksLikeImportLink(String? text) => looksLikeNodeLink(text) || looksLikeProfileLink(text);
