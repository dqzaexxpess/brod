import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/simple_rules.dart';

void main() {
  test('простые строки раскладываются по видам, смешивать виды в одном правиле нельзя', () {
    final r = parseSimpleLines('''
# комментарий
example.com
Sub.Example.ORG
domain:www.exact.example
keyword:tracker
regex:^ads\\d+\\.example\\.com\$
203.0.113.0/24
ip:198.51.100.7
2001:db8::1
process:firefox
port:443
port:8000-9000
protocol:TLS
geosite:youtube
''', knownSets: const ['geosite-youtube', 'geosite-telegram']);
    expect(r.errors, isEmpty);
    expect(r.groups['Сайты']!.domainSuffix, ['example.com', 'sub.example.org']);
    expect(r.groups['Точные домены']!.domain, ['www.exact.example']);
    expect(r.groups['Слова в домене']!.domainKeyword, ['tracker']);
    expect(r.groups['Регулярные выражения']!.domainRegex.single, startsWith('^ads'));
    expect(r.groups['IP-адреса']!.ipCidr, ['203.0.113.0/24', '198.51.100.7', '2001:db8::1'], reason: 'IPv6 не принимается за «префикс:значение»');
    expect(r.groups['Процессы']!.processName, ['firefox']);
    expect(r.groups['Порты']!.port, [443]);
    expect(r.groups['Порты']!.portRange, ['8000:9000']);
    expect(r.groups['Протоколы']!.protocol, ['tls']);
    expect(r.groups['Встроенные списки']!.ruleSet, ['geosite-youtube']);
    // каждый вид — отдельное правило: в одном «сайт И IP» не совпал бы никогда
    expect(r.groups.length, 9);
  });

  test('ошибки в строках не мешают остальным', () {
    final r = parseSimpleLines('foo\nbar:baz\nport:abc\ngeosite:nope\nip:\nok.example', knownSets: const ['geosite-youtube']);
    expect(r.groups['Сайты']!.domainSuffix, ['ok.example']);
    expect(r.errors.length, 5);
    expect(r.errors.any((e) => e.startsWith('bar:baz') && e.contains('неизвестный префикс')), isTrue);
    expect(r.errors.any((e) => e.startsWith('foo')), isTrue);
    expect(parseSimpleLines('   \n# только комментарий\n').groups, isEmpty);
  });
}
