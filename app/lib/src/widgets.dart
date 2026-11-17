import 'package:flutter/material.dart';

import 'api.dart';
import 'country.dart';

/// Небольшая «таблетка» с значком и текстом (пинг, скорость, чистота, метки).
class Pill extends StatelessWidget {
  const Pill({super.key, required this.text, this.icon, this.color, this.filled = false});

  final String text;
  final IconData? icon;
  final Color? color;
  final bool filled;

  @override
  Widget build(BuildContext context) {
    final c = color ?? Theme.of(context).colorScheme.onSurfaceVariant;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        color: filled ? c.withValues(alpha: 0.14) : null,
        border: filled ? null : Border.all(color: c.withValues(alpha: 0.7)),
        borderRadius: BorderRadius.circular(20),
      ),
      child: Row(mainAxisSize: MainAxisSize.min, children: [
        if (icon != null) ...[Icon(icon, size: 14, color: c), const SizedBox(width: 4)],
        Flexible(child: Text(text, style: TextStyle(fontSize: 12, color: c, fontWeight: FontWeight.w500))), // длинный текст переносится, а не вылезает за карточку
      ]),
    );
  }
}

Color pingColor(int ms, ColorScheme cs) => ms < 400 ? Colors.green : (ms < 1200 ? Colors.orange : cs.error);
Color speedColor(double mbps, ColorScheme cs) => mbps >= 10 ? Colors.green : (mbps >= 3 ? Colors.orange : cs.error);

({String label, Color? color}) cleanStyle(String clean, ColorScheme cs) => switch (clean) {
      'clean' => (label: 'IP чистый', color: Colors.green),
      'mixed' => (label: 'IP средний', color: Colors.orange),
      'dirty' => (label: 'IP грязный', color: cs.error),
      _ => (label: '', color: null),
    };

/// Метка «для звонков» по замеру UDP: голос и видео идут по UDP, а проверки HTTPS о нём ничего не говорят. Нет замера — метки нет.
({String label, IconData icon, Color color})? callsStyle(String udp, ColorScheme cs) => switch (udp) {
      'good' => (label: 'для звонков', icon: Icons.headset_mic_outlined, color: Colors.green),
      'unstable' => (label: 'звонки рвутся', icon: Icons.headset_off_outlined, color: Colors.orange),
      'none' => (label: 'нет UDP', icon: Icons.headset_off_outlined, color: cs.error),
      _ => null,
    };

/// Строка таблеток узла: пинг, скорость, чистота, YouTube, «мой».
class NodePills extends StatelessWidget {
  const NodePills({super.key, required this.node, this.showPing = true, this.leading = const []});

  final NodeDto node;
  final bool showPing; // пинг не показывается, если его число уже есть в другой таблетке (главная: «выход работает · N мс»)
  final List<Widget> leading; // таблетки перед остальными

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final e = node.exit;
    final cl = e == null ? null : cleanStyle(e.clean, cs);
    return Wrap(spacing: 6, runSpacing: 4, children: [
      ...leading,
      if (node.own) const Pill(text: 'мой', icon: Icons.star_rounded, color: Colors.purple, filled: true),
      if (showPing && node.latencyMs > 0) Pill(text: '${node.latencyMs} мс', icon: Icons.network_ping, color: pingColor(node.latencyMs, cs)),
      if (node.speedKBps > 0) Pill(text: '${node.speedMbps.toStringAsFixed(node.speedMbps < 10 ? 1 : 0)} Мбит/с', icon: Icons.speed, color: speedColor(node.speedMbps, cs)),
      if (callsStyle(node.udp, cs) case final c?) Pill(text: c.label, icon: c.icon, color: c.color, filled: node.udp == 'good'),
      if (cl != null && cl.label.isNotEmpty) Pill(text: cl.label, icon: Icons.verified_user_outlined, color: cl.color, filled: true),
      if (node.youtube) Pill(text: 'YouTube', icon: Icons.play_circle_outline, color: cs.primary),
      if (node.insecure) Pill(text: 'без проверки TLS', icon: Icons.warning_amber, color: cs.error),
    ]);
  }
}

/// Круглый «значок» страны: флаг на мягком фоне.
class FlagBadge extends StatelessWidget {
  const FlagBadge({super.key, required this.country, this.size = 44});

  final String? country;
  final double size;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Container(
      width: size,
      height: size,
      alignment: Alignment.center,
      decoration: BoxDecoration(shape: BoxShape.circle, color: cs.surfaceContainerHighest),
      child: Text(
        country == null || country!.isEmpty ? '🌐' : flagOf(country!),
        // где флаги не рисуются, вместо них код страны: буквы мельче и жирнее эмодзи
        style: flagEmojiSupported ? TextStyle(fontSize: size * 0.55) : TextStyle(fontSize: size * 0.34, fontWeight: FontWeight.w600, color: cs.onSurfaceVariant),
      ),
    );
  }
}


/// Единый вид уведомлений приложения: плавающая карточка над нижней панелью. Целиком строится из цветовой схемы текущей темы
/// (светлая / тёмная / AMOLED, выбранный акцент): фон — поверхность темы с лёгким оттенком акцента (у ошибки — цвета ошибки),
/// значок и рамка — акцент, текст — основной цвет темы. Успех висит 3 с, ошибка 6 с.
void showToast(BuildContext context, String text, {bool error = false}) {
  final cs = Theme.of(context).colorScheme;
  final accent = error ? cs.error : cs.primary;
  final m = ScaffoldMessenger.of(context);
  m.hideCurrentSnackBar();
  m.showSnackBar(SnackBar(
    behavior: SnackBarBehavior.floating,
    margin: const EdgeInsets.fromLTRB(16, 0, 16, 12),
    padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
    backgroundColor: Color.alphaBlend(accent.withValues(alpha: 0.12), cs.surfaceContainerHigh),
    elevation: 0, // без тени: на светлой и тёмной темах она давала тяжёлый чёрный ореол, рамки достаточно
    duration: Duration(seconds: error ? 6 : 3),
    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(14), side: BorderSide(color: accent.withValues(alpha: 0.45))),
    content: Row(children: [
      Icon(error ? Icons.error_outline : Icons.check_circle_outline, size: 20, color: accent),
      const SizedBox(width: 12),
      Expanded(child: Text(text, style: TextStyle(color: cs.onSurface, fontSize: 14))),
    ]),
  ));
}
