import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/conn_rule.dart';
import 'package:brod/src/model.dart';

ConnDto conn(String dest, {String network = 'udp', String process = ''}) =>
    ConnDto(id: '1', network: network, dest: dest, route: 'direct', node: '', process: process, started: DateTime(2026));

void main() {
  modelTests();
  directTests();
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

void directTests() {
  test('правила «напрямую»: те же цели, другое название', () {
    final o = connRuleOffers(conn('cdn.discordapp.com:443', network: 'tcp', process: 'Discord.exe'), action: connDirect);
    expect(o.length, 3);
    expect(o[0].title, 'Напрямую: Discord.exe');
    expect(o[1].title, 'Напрямую: cdn.discordapp.com');
    expect(o[0].match.processName, ['Discord.exe']);
    expect(connRulePrefix(connViaExit), 'Через выход: ');
  });
}

class _RulesApi implements DaemonApi {
  List<RouteRuleDto> saved = [RouteRuleDto(title: 'Через выход: example.org', action: 'proxy', match: RouteMatch(domainSuffix: ['example.org'])), RouteRuleDto(title: 'Другое', action: 'block')];

  @override
  Future<RouteRulesDto> routeRules() async => RouteRulesDto(saved, const []);

  @override
  Future<String?> putRouteRules(List<RouteRuleDto> rules) async {
    saved = rules;
    return null;
  }

  @override
  Future<List<UserSetDto>> userSets() async => const [];

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) async => null;
}

void modelTests() {
  test('«напрямую» ставится первым и убирает противоположное правило для той же цели', () async {
    final api = _RulesApi();
    final m = AppModel(api);
    final o = connRuleOffers(conn('example.org:443', network: 'tcp'), action: connDirect)[0];
    expect(o.title, 'Напрямую: example.org');
    expect(await m.sendConnDirect(o), true);
    expect(api.saved.map((r) => r.title).toList(), ['Напрямую: example.org', 'Другое']);
    expect(api.saved.first.action, 'direct');
    // и обратно: «через выход» убирает «напрямую»
    final back = connRuleOffers(conn('example.org:443', network: 'tcp'))[0];
    expect(await m.sendConnViaExit(back), true);
    expect(api.saved.map((r) => r.title).toList(), ['Через выход: example.org', 'Другое']);
    // повтор не плодит дубль
    await m.sendConnViaExit(back);
    expect(api.saved.length, 2);
  });
}
