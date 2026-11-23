/// Запись поиска по настройкам и функциям: название, куда вести и слова, по которым её ищут (синонимы, как называют люди).
class SearchEntry {
  const SearchEntry(this.title, this.section, this.keywords, {this.hint = '', this.id = '', this.page = false});
  final String title;
  final String section; // идентификатор раздела (имя значения _Sec)
  final String keywords; // слова через пробел
  final String hint; // одна строка пояснения под названием
  final String id; // якорь внутри раздела: к нему прокручивается экран, он подсвечивается; для page — что открыть
  final bool page; // true: пункт сам открывает страницу или окно (по id), раздел не нужен
}

String _norm(String s) => s.toLowerCase().replaceAll('ё', 'е').replaceAll(RegExp(r'[^a-zа-я0-9 ]'), ' ');

/// Слово запроса находится в тексте целиком или (для длинных слов) без последних двух букв: «серверы» находит «сервер», «обновления» — «обновление».
bool _has(String hay, String w) => hay.contains(w) || (w.length >= 6 && hay.contains(w.substring(0, w.length - 2)));

/// Записи, в которых встречается каждое слово запроса (как часть слова в названии, ключевых словах или пояснении); совпадения в названии выше.
/// Пустой запрос — пустой результат.
List<SearchEntry> searchSettings(String query, List<SearchEntry> all) {
  final words = _norm(query).split(' ').where((w) => w.isNotEmpty).toList();
  if (words.isEmpty) return const [];
  final scored = <(int, SearchEntry)>[];
  for (final e in all) {
    final t = _norm(e.title);
    final hay = '$t ${_norm(e.keywords)} ${_norm(e.hint)}';
    if (!words.every((w) => _has(hay, w))) continue;
    scored.add((words.every((w) => _has(t, w)) ? 0 : 1, e));
  }
  scored.sort((a, b) => a.$1.compareTo(b.$1)); // sort в Dart не устойчивая в общем случае; порядок внутри группы не важен
  return [for (final s in scored) s.$2];
}
