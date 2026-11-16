/// Размер в байтах: «1,5 МБ».
String fmtBytes(num b) {
  const u = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
  var v = b.toDouble();
  var i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  final s = i == 0 ? v.toStringAsFixed(0) : (v < 10 ? v.toStringAsFixed(1) : v.toStringAsFixed(0));
  return '${s.replaceAll('.', ',')} ${u[i]}';
}

/// Скорость в байтах в секунду: «1,5 МБ/с».
String fmtRate(num bps) => '${fmtBytes(bps)}/с';

/// Возраст: «5 с», «3 мин», «2 ч».
String fmtAge(DateTime t) {
  final d = DateTime.now().difference(t.toLocal());
  if (d.inSeconds < 60) return '${d.inSeconds < 0 ? 0 : d.inSeconds} с';
  if (d.inMinutes < 60) return '${d.inMinutes} мин';
  if (d.inHours < 24) return '${d.inHours} ч';
  return '${d.inDays} дн';
}
