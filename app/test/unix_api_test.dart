import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';

void main() {
  test('UnixApi говорит с сервером по unix-сокету', () async {
    final dir = await Directory.systemTemp.createTemp('vpnapp');
    final path = '${dir.path}/s.sock';
    final server = HttpServer.listenOn(await ServerSocket.bind(InternetAddress(path, type: InternetAddressType.unix), 0));
    server.listen((req) async {
      final body = await utf8.decodeStream(req);
      req.response.headers.contentType = ContentType.json;
      switch ('${req.method} ${req.uri.path}') {
        case 'GET /v1/vpn':
          req.response.write(jsonEncode({'on': true, 'mode': 'all', 'interface': 'vpnclient0'}));
        case 'POST /v1/vpn':
          expect(jsonDecode(body), {'on': false});
          req.response.statusCode = 409;
          req.response.write(jsonEncode({'error': 'нельзя'}));
        case 'GET /v1/status':
          req.response.write(jsonEncode({'version': 'v', 'nodes': 3, 'present': 2, 'degraded': false, 'active': {'proto': 'vless', 'server': 'h', 'port': 443}}));
        default:
          req.response.statusCode = 404;
      }
      await req.response.close();
    });
    final api = UnixApi(path);
    final v = await api.vpn();
    expect(v.on, true);
    expect(v.mode, 'all');
    expect(v.iface, 'vpnclient0');
    await expectLater(api.setVpn(on: false), throwsA(isA<ApiException>().having((e) => e.message, 'message', 'нельзя')));
    final s = await api.status();
    expect(s.active!.title, 'vless · h:443');
    api.close();
    await server.close(force: true);
    await dir.delete(recursive: true);
  });

  test('по таймауту запрос отменяется, соединение с зависшим демоном не копится', () async {
    final dir = await Directory.systemTemp.createTemp('vpnapp-to');
    final path = '${dir.path}/s.sock';
    final server = await ServerSocket.bind(InternetAddress(path, type: InternetAddressType.unix), 0);
    final closed = Completer<void>();
    server.listen((c) => c.listen((_) {}, onDone: () => closed.complete(), onError: (_) => closed.complete())); // принимает и молчит
    final api = UnixApi(path, readTimeout: const Duration(milliseconds: 300));
    await expectLater(api.status(), throwsA(isA<TimeoutException>()));
    await closed.future.timeout(const Duration(seconds: 3), onTimeout: () => fail('соединение осталось открытым после таймаута'));
    api.close();
    await server.close();
    await dir.delete(recursive: true);
  });
  test('список узлов не перечитывается, пока метка версии не изменилась (304), а при изменении разбирается заново', () async {
    final dir = await Directory.systemTemp.createTemp('vpnapp-etag');
    final path = '${dir.path}/s.sock';
    final server = HttpServer.listenOn(await ServerSocket.bind(InternetAddress(path, type: InternetAddressType.unix), 0));
    var gen = 1;
    final sent = <String?>[];
    var bodies = 0;
    server.listen((req) async {
      final tag = '"g$gen"';
      sent.add(req.headers.value('if-none-match'));
      req.response.headers.set('etag', tag);
      if (req.headers.value('if-none-match') == tag) {
        req.response.statusCode = 304;
      } else {
        bodies++;
        req.response.headers.contentType = ContentType.json;
        req.response.write(jsonEncode([
          {'id': 'n$gen', 'proto': 'vless', 'server': 'h', 'port': 443}
        ]));
      }
      await req.response.close();
    });
    final api = UnixApi(path);
    final a = await api.nodes(limit: 10);
    final b = await api.nodes(limit: 10);
    expect(sent, [null, '"g1"'], reason: 'второй запрос условный');
    expect(bodies, 1, reason: 'тело отправлено один раз');
    expect(b.map((n) => n.id), a.map((n) => n.id));
    gen = 2;
    final c = await api.nodes(limit: 10);
    expect(c.single.id, 'n2');
    expect(bodies, 2);
    api.close();
    await server.close(force: true);
    await dir.delete(recursive: true);
  });
}
