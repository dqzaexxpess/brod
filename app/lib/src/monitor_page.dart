import 'dart:io';
import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'android_bridge.dart';
import 'api.dart';
import 'conn_rule.dart';
import 'format.dart';
import 'model.dart';
import 'widgets.dart';

/// Монитор: график трафика, соединения, журнал (движок и события) — как в Throne.
class MonitorPage extends StatefulWidget {
  const MonitorPage({super.key, required this.model, required this.visible});

  final AppModel model;
  final bool visible;

  @override
  State<MonitorPage> createState() => _MonitorPageState();
}

class _MonitorPageState extends State<MonitorPage> with SingleTickerProviderStateMixin {
  late final TabController _tabs = TabController(length: 3, vsync: this);
  static const _names = ['traffic', 'conns', 'log'];

  @override
  void initState() {
    super.initState();
    _tabs.addListener(() {
      if (!_tabs.indexIsChanging) widget.model.setMonitor(tab: _names[_tabs.index]);
    });
    if (widget.visible) Future.microtask(() => widget.model.setMonitor(on: true, tab: _names[_tabs.index]));
  }

  @override
  void didUpdateWidget(MonitorPage old) {
    super.didUpdateWidget(old);
    if (widget.visible != old.visible) Future.microtask(() => widget.model.setMonitor(on: widget.visible, tab: _names[_tabs.index]));
  }

  @override
  void dispose() {
    widget.model.setMonitor(on: false);
    _tabs.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Column(children: [
      TabBar(controller: _tabs, tabs: const [Tab(text: 'Трафик'), Tab(text: 'Соединения'), Tab(text: 'Журнал')]),
      Expanded(
        child: TabBarView(controller: _tabs, children: [
          _TrafficTab(model: widget.model),
          _ConnsTab(model: widget.model),
          _LogTab(model: widget.model),
        ]),
      ),
    ]);
  }
}

// ---------------- график ----------------

class _TrafficTab extends StatelessWidget {
  const _TrafficTab({required this.model});

  final AppModel model;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final t = model.traffic;
        final cs = Theme.of(context).colorScheme;
        return ListView(padding: const EdgeInsets.all(16), children: [
          Row(children: [
            Expanded(child: _stat(context, Icons.arrow_downward, 'Загрузка', fmtRate(t.downNow), cs.primary)),
            const SizedBox(width: 12),
            Expanded(child: _stat(context, Icons.arrow_upward, 'Отдача', fmtRate(t.upNow), cs.tertiary)),
          ]),
          const SizedBox(height: 12),
          Card.outlined(
            child: Padding(
              padding: const EdgeInsets.fromLTRB(12, 12, 12, 8),
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Text('Последние ${t.samples.length} с', style: Theme.of(context).textTheme.labelMedium),
                const SizedBox(height: 8),
                SizedBox(height: 180, child: CustomPaint(painter: TrafficPainter(t.samples, cs.primary, cs.tertiary, cs.outlineVariant, cs.onSurfaceVariant), size: Size.infinite)),
              ]),
            ),
          ),
          const SizedBox(height: 12),
          _DailyCard(model: model),
          const SizedBox(height: 12),
          Card.outlined(
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                _kv(context, 'Скачано с запуска', fmtBytes(t.downTotal)),
                const SizedBox(height: 6),
                _kv(context, 'Отправлено с запуска', fmtBytes(t.upTotal)),
                const SizedBox(height: 6),
                _kv(context, 'Открытых соединений', '${t.conns}'),
              ]),
            ),
          ),
        ]);
      },
    );
  }

  Widget _stat(BuildContext context, IconData i, String k, String v, Color c) => Card.filled(
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Row(children: [
            Icon(i, color: c),
            const SizedBox(width: 10),
            Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(k, style: Theme.of(context).textTheme.labelSmall),
              Text(v, style: Theme.of(context).textTheme.titleMedium),
            ]),
          ]),
        ),
      );

  Widget _kv(BuildContext context, String k, String v) => Row(children: [
        Expanded(child: Text(k, style: Theme.of(context).textTheme.bodySmall)),
        Text(v, style: Theme.of(context).textTheme.bodyMedium),
      ]);
}

/// График скорости: загрузка и отдача, шкала по максимуму окна.
class TrafficPainter extends CustomPainter {
  TrafficPainter(this.samples, this.down, this.up, this.grid, this.label);

  final List<TrafficSample> samples;
  final Color down;
  final Color up;
  final Color grid;
  final Color label;

  @override
  void paint(Canvas canvas, Size size) {
    const left = 54.0;
    final w = size.width - left, h = size.height - 4;
    final peak = math.max(1024, samples.fold<int>(0, (m, s) => math.max(m, math.max(s.up, s.down)))).toDouble();
    final top = _niceTop(peak);
    final gp = Paint()..color = grid..strokeWidth = 1;
    for (var i = 0; i <= 4; i++) {
      final y = 2 + h * i / 4;
      canvas.drawLine(Offset(left, y), Offset(size.width, y), gp);
      final tp = TextPainter(text: TextSpan(text: fmtRate(top * (4 - i) / 4), style: TextStyle(fontSize: 10, color: label)), textDirection: TextDirection.ltr)..layout();
      tp.paint(canvas, Offset(0, y - tp.height / 2));
    }
    if (samples.length < 2) return;
    void line(int Function(TrafficSample) f, Color c) {
      final n = samples.length;
      final path = Path();
      for (var i = 0; i < n; i++) {
        final x = left + w * i / math.max(n - 1, 1);
        final y = 2 + h * (1 - f(samples[i]) / top);
        i == 0 ? path.moveTo(x, y) : path.lineTo(x, y);
      }
      final fill = Path.from(path)..lineTo(left + w, 2 + h)..lineTo(left, 2 + h)..close();
      canvas.drawPath(fill, Paint()..color = c.withValues(alpha: 0.15));
      canvas.drawPath(path, Paint()..color = c..style = PaintingStyle.stroke..strokeWidth = 2..strokeJoin = StrokeJoin.round);
    }

    line((s) => s.down, down);
    line((s) => s.up, up);
  }

  /// Округление верха шкалы вверх до 1/2/5·10^k.
  static double _niceTop(double v) {
    final p = math.pow(10, (math.log(v) / math.ln10).floor()).toDouble();
    for (final m in [1.0, 2.0, 5.0, 10.0]) {
      if (v <= m * p) return m * p;
    }
    return 10 * p;
  }

  @override
  bool shouldRepaint(TrafficPainter old) => old.samples != samples;
}

// ---------------- соединения ----------------

class _ConnsTab extends StatelessWidget {
  const _ConnsTab({required this.model});

  final AppModel model;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final list = model.visibleConns;
        final cs = Theme.of(context).colorScheme;
        return Column(children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
            child: Row(children: [
              Expanded(
                child: TextField(
                  decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: 'Поиск: адрес, процесс, узел', isDense: true, border: OutlineInputBorder()),
                  onChanged: (v) => model.setConnsView(search: v),
                ),
              ),
              const SizedBox(width: 8),
              FilterChip(label: const Text('+ закрытые'), selected: model.connsClosed, onSelected: (v) => model.setConnsView(closed: v)),
            ]),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 4),
            child: Align(alignment: Alignment.centerLeft, child: Text('Показано: ${list.length}', style: Theme.of(context).textTheme.labelSmall?.copyWith(color: cs.onSurfaceVariant))),
          ),
          Expanded(
            child: list.isEmpty
                ? const Center(child: Text('Соединений нет'))
                : ListView.builder(
                    itemCount: list.length,
                    itemBuilder: (context, i) => _ConnTile(model: model, c: list[i]),
                  ),
          ),
        ]);
      },
    );
  }
}

Color routeColor(String route, ColorScheme cs) => switch (route) {
      'direct' => Colors.green,
      'proxy' => cs.primary,
      'target' => Colors.purple,
      'block' => cs.error,
      _ => cs.outline,
    };

String routeName(ConnDto c) => switch (c.route) {
      'direct' => 'напрямую',
      'proxy' => 'VPN',
      'target' => 'цель ${c.routeName}',
      'block' => 'блок',
      _ => c.route,
    };

class _ConnTile extends StatelessWidget {
  const _ConnTile({required this.model, required this.c});

  final AppModel model;
  final ConnDto c;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final col = routeColor(c.route, cs);
    return ListTile(
      dense: true,
      title: Text(c.dest, overflow: TextOverflow.ellipsis, style: TextStyle(color: c.open ? null : cs.onSurfaceVariant)),
      subtitle: Wrap(spacing: 8, crossAxisAlignment: WrapCrossAlignment.center, children: [
        Container(
          padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
          decoration: BoxDecoration(border: Border.all(color: col), borderRadius: BorderRadius.circular(6)),
          child: Text(routeName(c), style: TextStyle(fontSize: 11, color: col)),
        ),
        Text('${c.network.toUpperCase()} · ${c.open ? fmtAge(c.started) : 'закрыто'}${c.process.isEmpty ? '' : ' · ${c.process}'}', style: const TextStyle(fontSize: 12)),
      ]),
      trailing: Column(mainAxisAlignment: MainAxisAlignment.center, crossAxisAlignment: CrossAxisAlignment.end, children: [
        Text('↓ ${fmtBytes(c.down)}', style: const TextStyle(fontSize: 12)),
        Text('↑ ${fmtBytes(c.up)}', style: TextStyle(fontSize: 12, color: cs.onSurfaceVariant)),
      ]),
      onTap: () => showModalBottomSheet<void>(context: context, showDragHandle: true, builder: (_) => _ConnDetails(model: model, c: c)),
    );
  }
}

class _ConnDetails extends StatelessWidget {
  const _ConnDetails({required this.model, required this.c});

  final AppModel model;
  final ConnDto c;

  /// Кнопки правил для соединения: что именно запомнить (процесс, домен, адрес, порт) и действие.
  List<Widget> _offers(BuildContext context, String caption, String action, IconData icon) => [
        const SizedBox(height: 12),
        Text(caption, style: Theme.of(context).textTheme.bodySmall),
        for (final o in connRuleOffers(c, action: action))
          Align(
            alignment: Alignment.centerLeft,
            child: TextButton.icon(
              icon: Icon(icon),
              label: Text(o.label),
              onPressed: () {
                model.sendConnRule(o, action);
                Navigator.pop(context);
              },
            ),
          ),
      ];

  @override
  Widget build(BuildContext context) {
    Widget row(String k, String v) => Padding(
          padding: const EdgeInsets.symmetric(vertical: 3),
          child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
            SizedBox(width: 110, child: Text(k, style: Theme.of(context).textTheme.bodySmall)),
            Expanded(child: SelectableText(v)),
          ]),
        );
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 0, 20, 24),
      child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
        row('Адрес', c.dest),
        row('Маршрут', '${routeName(c)} · ${c.node}'),
        row('Вход', '${c.inbound} (${c.network.toUpperCase()})'),
        row('Откуда', c.source),
        if (c.process.isNotEmpty) row('Процесс', c.process),
        row('Трафик', '↓ ${fmtBytes(c.down)}   ↑ ${fmtBytes(c.up)}'),
        row('Открыто', '${fmtAge(c.started)} назад${c.open ? '' : ' (закрыто)'}'),
        if (c.route != 'proxy' && connRuleOffers(c).isNotEmpty) ..._offers(context, 'Отправить через выход (VPN перезапустится):', connViaExit, Icons.alt_route),
        if (c.route != 'direct' && connRuleOffers(c, action: connDirect).isNotEmpty) ..._offers(context, 'Пускать напрямую, мимо VPN (VPN перезапустится):', connDirect, Icons.arrow_outward),
        if (c.open) ...[
          const SizedBox(height: 12),
          FilledButton.tonalIcon(
            icon: const Icon(Icons.link_off),
            label: const Text('Закрыть соединение'),
            onPressed: () {
              model.closeConn(c);
              Navigator.pop(context);
            },
          ),
        ],
      ]),
    );
  }
}

// ---------------- журнал ----------------

class _LogTab extends StatefulWidget {
  const _LogTab({required this.model});

  final AppModel model;

  @override
  State<_LogTab> createState() => _LogTabState();
}

class _LogTabState extends State<_LogTab> {
  static const _maxLines = 1000; // больше экран всё равно не прочитает, а каждая строка — виджет

  bool _events = true;
  String _q = '';
  bool _follow = true;
  List<String>? _frozen; // снимок на момент паузы: пока «не прокручивать за новыми», список не меняется под пальцем
  bool _frozenEvents = true;

  /// Строки источника (события или лог движка), либо замороженный снимок.
  List<String> _source(AppModel m) {
    if (!_follow && _frozen != null && _frozenEvents == _events) return _frozen!;
    final lines = _events
        ? [for (final e in m.eventList) '${e.at.toLocal().toIso8601String().substring(11, 19)}  ${e.kind}${e.initiator.isEmpty ? '' : ' [${e.initiator}]'}  ${e.detail}']
        : m.engineLines;
    return lines.length > _maxLines ? lines.sublist(lines.length - _maxLines) : lines;
  }

  Color _lineColor(String l, ColorScheme cs) {
    if (l.contains(' ERROR ') || l.contains(' FATAL ')) return cs.error;
    if (l.contains(' WARN ')) return Colors.orange;
    return cs.onSurface;
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final cs = Theme.of(context).colorScheme;
        final q = _q.trim().toLowerCase();
        final lines = _source(m);
        final shown = q.isEmpty ? lines : lines.where((l) => l.toLowerCase().contains(q)).toList();
        return Column(children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
            // на узком экране (телефон) поле фильтра уходит во вторую строку: в одной строке с переключателем и кнопками ему не остаётся места
            child: LayoutBuilder(builder: (context, c) {
              final narrow = c.maxWidth < 560;
              final source = SegmentedButton<bool>(
                showSelectedIcon: false,
                segments: const [ButtonSegment(value: true, label: Text('События')), ButtonSegment(value: false, label: Text('Движок'))],
                selected: {_events},
                onSelectionChanged: (s) => setState(() {
                  _events = s.first;
                  if (!_follow) _follow = true; // смена источника снимает паузу: снимок относился к другому списку
                  _frozen = null;
                }),
              );
              final field = TextField(
                decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: 'Фильтр', isDense: true, border: OutlineInputBorder()),
                onChanged: (v) => setState(() => _q = v),
              );
              final buttons = <Widget>[
                IconButton(tooltip: _follow ? 'Не прокручивать за новыми' : 'Прокручивать за новыми', icon: Icon(_follow ? Icons.vertical_align_bottom : Icons.pause), onPressed: () => setState(() {
                      if (_follow) {
                        _frozen = _source(m); // пауза: фиксируем то, что на экране
                        _frozenEvents = _events;
                      } else {
                        _frozen = null;
                      }
                      _follow = !_follow;
                    })),
                IconButton(
                  tooltip: 'Копировать показанное',
                  icon: const Icon(Icons.copy),
                  onPressed: () {
                    Clipboard.setData(ClipboardData(text: shown.join('\n')));
                    showToast(context, 'Скопировано');
                  },
                ),
                if (Platform.isAndroid) IconButton(tooltip: 'Поделиться журналом', icon: const Icon(Icons.share), onPressed: AndroidCore.shareLogs),
              ];
              if (narrow) {
                return Column(children: [
                  Row(children: [source, const Spacer(), ...buttons]),
                  const SizedBox(height: 8),
                  field,
                ]);
              }
              return Row(children: [source, const SizedBox(width: 8), Expanded(child: field), ...buttons]);
            }),
          ),
          Expanded(
            child: shown.isEmpty
                ? const Center(child: Text('Пусто'))
                // от конца: новые строки появляются внизу сами, без принудительной прокрутки; одна область выделения на весь список
                : SelectionArea(
                    child: ListView.builder(
                      reverse: true,
                      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
                      itemCount: shown.length,
                      itemBuilder: (context, i) {
                        final line = shown[shown.length - 1 - i];
                        return Text(line, style: TextStyle(fontFamily: 'monospace', fontSize: 11.5, height: 1.35, color: _lineColor(line, cs)));
                      },
                    ),
                  ),
          ),
        ]);
      },
    );
  }
}

/// Трафик по дням: столбики, внутри — доля через VPN.
class _DailyCard extends StatefulWidget {
  const _DailyCard({required this.model});

  final AppModel model;

  @override
  State<_DailyCard> createState() => _DailyCardState();
}

class _DailyCardState extends State<_DailyCard> {
  Timer? _t;

  @override
  void initState() {
    super.initState();
    Future.microtask(widget.model.loadDaily);
    _t = Timer.periodic(const Duration(seconds: 30), (_) => widget.model.loadDaily());
  }

  @override
  void dispose() {
    _t?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final days = widget.model.daily;
    final cs = Theme.of(context).colorScheme;
    final peak = days.fold<int>(1, (m, d) => math.max(m, d.total));
    return Card.outlined(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            Expanded(child: Text('По дням', style: Theme.of(context).textTheme.labelLarge)),
            _legend(cs.primary, 'через VPN'),
            const SizedBox(width: 12),
            _legend(cs.outlineVariant, 'напрямую'),
          ]),
          const SizedBox(height: 12),
          if (days.isEmpty)
            const Padding(padding: EdgeInsets.symmetric(vertical: 20), child: Center(child: Text('Данные копятся по мере работы')))
          else
            SizedBox(
              height: 140,
              child: Row(crossAxisAlignment: CrossAxisAlignment.end, children: [
                for (final d in days)
                  Expanded(
                    child: Tooltip(
                      message: '${d.day}\nвсего ${fmtBytes(d.total)}\nчерез VPN ${fmtBytes(d.vpnTotal)}',
                      child: Padding(
                        padding: const EdgeInsets.symmetric(horizontal: 2),
                        child: Column(mainAxisAlignment: MainAxisAlignment.end, children: [
                          Flexible(
                            child: FractionallySizedBox(
                              heightFactor: math.max(d.total / peak, 0.02),
                              child: Column(children: [
                                Expanded(flex: math.max(d.total - d.vpnTotal, 0) + 1, child: Container(decoration: BoxDecoration(color: cs.outlineVariant, borderRadius: const BorderRadius.vertical(top: Radius.circular(4))))),
                                Expanded(flex: d.vpnTotal + 1, child: Container(color: cs.primary)),
                              ]),
                            ),
                          ),
                          const SizedBox(height: 4),
                          Text(d.day.substring(8), style: TextStyle(fontSize: 10, color: cs.onSurfaceVariant)),
                        ]),
                      ),
                    ),
                  ),
              ]),
            ),
        ]),
      ),
    );
  }

  Widget _legend(Color c, String t) => Row(mainAxisSize: MainAxisSize.min, children: [
        Container(width: 10, height: 10, decoration: BoxDecoration(color: c, borderRadius: BorderRadius.circular(2))),
        const SizedBox(width: 4),
        Text(t, style: const TextStyle(fontSize: 11)),
      ]);
}
