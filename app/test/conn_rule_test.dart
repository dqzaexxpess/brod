import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/conn_rule.dart';

ConnDto conn(String dest, {String network = 'udp', String process = ''}) =>
    ConnDto(id: '1', network: network, dest: dest, route: 'direct', node: '', process: process, started: DateTime(2026));

void main() {
  test('адрес и порт разбираются, в том числе IPv6', () {
    expect(splitDest('104.29.153.159:19313')!.port, 19313);
    expect(splitDest('[2001:db8::1]:443')!.host, '2001:db8::1');
    expect(splitDest('cdn.discordapp.com:443')!.host, 'cdn.discordapp.com');
    expect(splitDest('без порта'), isNull);
    expect(splitDest('host:99999'), isNull);
  });

  test('голосовой сервер без домена: адрес и UDP-порт', () {
    final o = connRuleOffers(conn('104.29.153.159:19313'));
    expect(o.length, 2);
    expect(o[0].match.ipCidr, ['104.29.153.159/32']);
    expect(o[1].match.network, 'udp');
    expect(o[1].match.port, [19313]);
  });

  test('с доменом и процессом: процесс, домен, порт', () {
    final o = connRuleOffers(conn('cdn.discordapp.com:443', network: 'tcp', process: 'Discord.exe'));
    expect(o.length, 3);
    expect(o[0].match.processName, ['Discord.exe']);
    expect(o[1].match.domainSuffix, ['cdn.discordapp.com']);
    expect(o[2].match.network, 'tcp');
  });

  test('IPv6 получает маску /128', () {
    expect(connRuleOffers(conn('[2001:db8::1]:443'))[0].match.ipCidr, ['2001:db8::1/128']);
  });
}
