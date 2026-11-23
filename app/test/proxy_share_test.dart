import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';

void main() {
  test('раздача прокси: данные читаются из ответа демона, адреса собираются с портом', () {
    final s = ProxyShareDto.fromJson({
      'addr': '127.0.0.1:29080',
      'username': 'brod',
      'password': 'secret',
      'lan': true,
      'lan_addrs': ['192.168.1.5', '10.0.0.7'],
      'port': 29080,
    });
    expect(s.lan, true);
    expect(s.endpoints, ['192.168.1.5:29080', '10.0.0.7:29080']);
    expect(s.username, 'brod');
    final off = ProxyShareDto.fromJson({'addr': '127.0.0.1:29080', 'lan': false, 'lan_addrs': [], 'port': 29080});
    expect(off.lan, false);
    expect(off.endpoints, isEmpty);
    expect(ProxyShareDto.fromJson({}).port, 0);
  });
}
