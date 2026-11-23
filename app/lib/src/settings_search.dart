/// Запись поиска по настройкам и функциям: название, куда вести и слова, по которым её ищут (синонимы, как называют люди).
class SearchEntry {
  const SearchEntry(this.title, this.section, this.keywords, {this.hint = ''});
  final String title;
  final String section; // идентификатор раздела (имя значения _Sec)
  final String keywords; // слова через пробел
  final String hint; // одна строка пояснения под названием
}

String _norm(String s) => s.toLowerCase().replaceAll('ё', 'е').replaceAll(RegExp(r'[^a-zа-я0-9 ]'), ' ');

/// Записи, в которых встречается каждое слово запроса (как часть слова в названии или ключевых словах); совпадения в названии выше.
/// Пустой запрос — пустой результат.
List<SearchEntry> searchSettings(String query, List<SearchEntry> all) {
  final words = _norm(query).split(' ').where((w) => w.isNotEmpty).toList();
  if (words.isEmpty) return const [];
  final scored = <(int, SearchEntry)>[];
  for (final e in all) {
    final t = _norm(e.title);
    final hay = '$t ${_norm(e.keywords)} ${_norm(e.hint)}';
    if (!words.every(hay.contains)) continue;
    scored.add((words.every(t.contains) ? 0 : 1, e));
  }
  scored.sort((a, b) => a.$1.compareTo(b.$1)); // sort в Dart не устойчивая в общем случае; порядок внутри группы не важен
  return [for (final s in scored) s.$2];
}
