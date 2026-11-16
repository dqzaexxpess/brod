import 'api.dart';

/// Результат разбора списка строк: по одному набору условий на вид (разные виды в одном правиле складываются по «И», поэтому смешивать их нельзя).
class SimpleParse {
  SimpleParse(this.groups, this.errors);

  /// Название вида → условия.
  final Map<String, RouteMatch> groups;
  final List<String> errors; // строки, которые не удалось разобрать, с пояснением
}

final _ipv4 = RegExp(r'^\d{1,3}(\.\d{1,3}){3}(/\d{1,2})?$');
final _ipv6 = RegExp(r'^[0-9a-fA-F:]*:[0-9a-fA-F:.]*(/\d{1,3})?$');
final _domain = RegExp(r'^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$');
final _portRange = RegExp(r'^(\d{1,5})[-:](\d{1,5})$');

/// Разбирает простые правила построчно: «example.com» (сайт с поддоменами), «domain:www.example.com», «suffix:…», «keyword:…», «regex:…»,
/// «ip:203.0.113.0/24» (или просто адрес/подсеть), «process:firefox», «port:443» / «port:8000-9000», «protocol:tls», «geosite:youtube»
/// (встроенный список `geosite-youtube`, если он есть среди [knownSets]). Пустые строки и строки с «#» в начале пропускаются.
SimpleParse parseSimpleLines(String text, {List<String> knownSets = const []}) {
  final groups = <String, RouteMatch>{};
  final errors = <String>[];
  RouteMatch g(String kind) => groups.putIfAbsent(kind, RouteMatch.new);
  for (final raw in text.split(RegExp(r'[\r\n]+'))) {
    final line = raw.trim();
    if (line.isEmpty || line.startsWith('#')) continue;
    final i = line.indexOf(':');
    var prefix = '';
    var value = line;
    // «ip:203.0.113.5» и «geosite:youtube» — префикс; но IPv6 («2001:db8::1») префиксом не считается
    if (i > 0 && !_ipv6.hasMatch(line) && RegExp(r'^[a-z_]+$', caseSensitive: false).hasMatch(line.substring(0, i))) {
      prefix = line.substring(0, i).toLowerCase();
      value = line.substring(i + 1).trim();
    }
    if (value.isEmpty) {
      errors.add('$line: пустое значение');
      continue;
    }
    switch (prefix) {
      case '':
        if (_ipv4.hasMatch(value) || _ipv6.hasMatch(value)) {
          g('IP-адреса').ipCidr.add(value);
        } else if (_domain.hasMatch(value.toLowerCase())) {
          g('Сайты').domainSuffix.add(value.toLowerCase());
        } else {
          errors.add('$line: не похоже ни на адрес, ни на домен (добавьте префикс, например keyword:)');
        }
      case 'domain' || 'full':
        g('Точные домены').domain.add(value.toLowerCase());
      case 'suffix' || 'domain_suffix':
        g('Сайты').domainSuffix.add(value.toLowerCase());
      case 'keyword' || 'domain_keyword':
        g('Слова в домене').domainKeyword.add(value.toLowerCase());
      case 'regex' || 'regexp' || 'domain_regex':
        g('Регулярные выражения').domainRegex.add(value);
      case 'ip' || 'cidr' || 'ip_cidr':
        g('IP-адреса').ipCidr.add(value);
      case 'process' || 'proc' || 'process_name':
        g('Процессы').processName.add(value);
      case 'port':
        final r = _portRange.firstMatch(value);
        final n = int.tryParse(value);
        if (r != null) {
          g('Порты').portRange.add('${r.group(1)}:${r.group(2)}');
        } else if (n != null) {
          g('Порты').port.add(n);
        } else {
          errors.add('$line: порт или диапазон (443, 8000-9000)');
        }
      case 'protocol':
        g('Протоколы').protocol.add(value.toLowerCase());
      case 'geosite' || 'ruleset' || 'set':
        final v = value.toLowerCase();
        final name = knownSets.contains(v) ? v : (knownSets.contains('geosite-$v') ? 'geosite-$v' : '');
        if (name.isEmpty) {
          errors.add('$line: нет встроенного списка «$value»');
        } else {
          g('Встроенные списки').ruleSet.add(name);
        }
      default:
        errors.add('$line: неизвестный префикс «$prefix:»');
    }
  }
  return SimpleParse(groups, errors);
}
