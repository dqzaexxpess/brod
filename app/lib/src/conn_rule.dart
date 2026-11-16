import 'api.dart';

/// Способ отправить соединение через выход правилом: что именно запомнить (домен, адрес, порт с протоколом, процесс).
class ConnRuleOffer {
  const ConnRuleOffer(this.label, this.title, this.match);

  final String label; // подпись кнопки
  final String title; // название создаваемого правила
  final RouteMatch match;
}

/// Разбор «адрес:порт» из журнала соединений: IPv6 приходит в скобках («[2001:db8::1]:443»).
({String host, int port})? splitDest(String dest) {
  final i = dest.lastIndexOf(':');
  if (i <= 0) return null;
  final port = int.tryParse(dest.substring(i + 1));
  if (port == null || port < 1 || port > 65535) return null;
  var host = dest.substring(0, i);
  if (host.startsWith('[') && host.endsWith(']')) host = host.substring(1, host.length - 1);
  return host.isEmpty ? null : (host: host, port: port);
}

bool _isIp(String host) => RegExp(r'^\d{1,3}(\.\d{1,3}){3}$').hasMatch(host) || host.contains(':');

/// Варианты правила «через выход» для соединения. Домен предпочтительнее адреса: адрес сервиса меняется, а голосовые серверы,
/// у которых домена в соединении нет (Discord), ловятся протоколом и портом или процессом.
List<ConnRuleOffer> connRuleOffers(ConnDto c) {
  final d = splitDest(c.dest);
  if (d == null) return const [];
  final out = <ConnRuleOffer>[];
  if (c.process.isNotEmpty) {
    out.add(ConnRuleOffer('Весь трафик процесса ${c.process}', 'Через выход: ${c.process}', RouteMatch(processName: [c.process])));
  }
  if (_isIp(d.host)) {
    final bits = d.host.contains(':') ? 128 : 32;
    out.add(ConnRuleOffer('Адрес ${d.host}', 'Через выход: ${d.host}', RouteMatch(ipCidr: ['${d.host}/$bits'])));
  } else {
    out.add(ConnRuleOffer('Домен ${d.host} и поддомены', 'Через выход: ${d.host}', RouteMatch(domainSuffix: [d.host])));
  }
  final net = c.network.toLowerCase();
  if (net == 'tcp' || net == 'udp') {
    out.add(ConnRuleOffer('${net.toUpperCase()}, порт ${d.port} (к любому адресу)', 'Через выход: ${net.toUpperCase()} ${d.port}', RouteMatch(network: net, port: [d.port])));
  }
  return out;
}
