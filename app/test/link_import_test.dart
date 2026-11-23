import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/link_import.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/prefs.dart';

class _Api implements DaemonApi {
  final added = <String>[];

  @override
  Future<String> addOwn(String text) async {
    added.add(text);
    return 'Добавлено: 1';
  }

  @override
  Future<List<NodeDto>> nodes({bool alive = false, bool favorites = false, int limit = 300, String source = ''}) async => const [];

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

void main() {
  test('ссылка на узел узнаётся по схеме; мусор, многострочный текст и пробелы отвергаются', () {
    for (final ok in ['vless://uuid@host.example:443?type=ws#name', 'VMESS://abcd', 'trojan://pw@h:1', 'ss://YWJj@h:2', 'hysteria2://a@h:3', 'hy2://a@h:3', 'tuic://a:b@h:4', 'wireguard://k@h:5']) {
      expect(looksLikeNodeLink(ok), true, reason: ok);
    }
    for (final bad in [null, '', 'https://example.org', 'vless://', 'vless:// host', 'vless://a@h:1\nvless://b@h:2', 'просто текст', 'ftp://x', 'x' * 5000]) {
      expect(looksLikeNodeLink(bad), false, reason: '$bad');
    }
  });

  test('в вопросе «добавить?» только протокол и адрес, без секретов', () {
    expect(nodeLinkSummary('vless://11111111-2222-3333-4444-555555555555@host.example:443?type=ws&security=tls#имя'), 'vless · host.example:443');
    expect(nodeLinkSummary('trojan://s3cr3t@h.example:8443'), 'trojan · h.example:8443');
    expect(nodeLinkSummary('vmess://eyJhZGQiOiJob3N0In0='), 'vmess', reason: 'vmess — base64: адрес не показываем');
    expect(nodeLinkSummary('ss://YWJjOnBhc3M@h.example:1#x').contains('pass'), false);
  });

  test('модель: ссылка предлагается; из буфера — один раз на ссылку; чужой текст не предлагается', () {
    final m = AppModel(_Api());
    m.offerImport('просто текст');
    expect(m.importOffer, isNull);
    m.offerImport('vless://u@h.example:443', fromClipboard: true);
    expect(m.importOffer, 'vless://u@h.example:443');
    m.importOffer = null; // пользователь ответил
    m.offerImport('vless://u@h.example:443', fromClipboard: true);
    expect(m.importOffer, isNull, reason: 'та же ссылка из буфера второй раз не предлагается');
    m.offerImport('vless://u@h.example:443'); // из другого приложения — всегда
    expect(m.importOffer, 'vless://u@h.example:443');
  });

  test('настройка предложения из буфера обмена сохраняется и по умолчанию включена', () {
    final p = Prefs();
    expect(p.clipboardImport, true);
    p.update(clipboardImport: false);
    expect(p.clipboardImport, false);
  });
}
