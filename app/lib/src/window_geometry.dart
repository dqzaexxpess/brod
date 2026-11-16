import 'dart:ui';

/// Прямоугольник окна, который можно вернуть на экран: он должен заметно пересекаться хотя бы с одним из мониторов (иначе монитор
/// отключили или сменили разрешение, и окно оказалось бы за краем экрана), а размер — быть не меньше минимального.
/// Подходящего нет — null, и окно остаётся там, где его поставила система.
Rect? restorableBounds(Rect saved, List<Rect> displays, {Size min = const Size(360, 300)}) {
  if (saved.width < min.width || saved.height < min.height) return null;
  for (final d in displays) {
    final overlap = saved.intersect(d);
    if (overlap.width >= 120 && overlap.height >= 80) return saved;
  }
  return null;
}

Map<String, double> boundsToJson(Rect r) => {'x': r.left, 'y': r.top, 'w': r.width, 'h': r.height};

Rect? boundsFromJson(Object? j) {
  if (j is! Map) return null;
  double? n(String k) => (j[k] is num && (j[k] as num).isFinite) ? (j[k] as num).toDouble() : null;
  final x = n('x'), y = n('y'), w = n('w'), h = n('h');
  if (x == null || y == null || w == null || h == null) return null;
  return Rect.fromLTWH(x, y, w, h);
}
