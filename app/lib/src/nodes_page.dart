import 'dart:async';

import 'package:flutter/material.dart';

import 'api.dart';
import 'country.dart';
import 'model.dart';
import 'node_dialogs.dart';
import 'qr_dialog.dart';
import 'prefs.dart';
import 'widgets.dart';

/// Список узлов: поиск, фильтры, сортировка, группы по странам, выбор активного узла, избранное, пакетные замеры.
class NodesPage extends StatefulWidget {
  const NodesPage({super.key, required this.model, required this.visible, this.prefs});

  final AppModel model;
  final bool visible;
  final Prefs? prefs;

  @override
  State<NodesPage> createState() => _NodesPageState();
}

class _NodesPageState extends State<NodesPage> {
  Timer? _timer;
  final _search = TextEditingController();
  final Prefs _fallback = Prefs();

  Prefs get _prefs => widget.prefs ?? _fallback;

  @override
  void initState() {
    super.initState();
    if (widget.visible) _activate();
  }

  @override
  void didUpdateWidget(NodesPage old) {
    super.didUpdateWidget(old);
    if (widget.visible && !old.visible) _activate();
    if (!widget.visible) _timer?.cancel();
  }

  void _activate() {
    scheduleMicrotask(widget.model.loadNodes); // не во время сборки: loadNodes сообщает слушателям синхронно
    _timer?.cancel();
    // пока идёт обновление или проверка (в том числе плановая, запущенная демоном), список перечитывается чаще: живые появляются по ходу
    var tick = 0;
    _timer = Timer.periodic(const Duration(seconds: 3), (_) {
      if (!widget.model.foreground) return; // приложение не на экране: список не перечитываем
      tick++;
      final busy = widget.model.status?.activity.busy ?? false;
      if (busy) {
        widget.model.loadNodes(quiet: true);
      } else if (tick % 5 == 0) {
        widget.model.loadNodes();
      }
    });
  }

  @override
  void dispose() {
    _timer?.cancel();
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: Listenable.merge([m, _prefs]),
      builder: (context, _) {
        final list = m.visibleNodes;
        final activeId = m.status?.active?.id ?? '';
        final cs = Theme.of(context).colorScheme;
        return Column(children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 12, 8, 4),
            child: Row(children: [
              Expanded(
                child: TextField(
                  controller: _search,
                  decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: 'Поиск: адрес, название, протокол', isDense: true),
                  onChanged: (v) => m.setNodeView(query: v),
                ),
              ),
              PopupMenuButton<NodeSort>(
                tooltip: 'Сортировка',
                icon: const Icon(Icons.sort),
                initialValue: m.sort,
                onSelected: (s) => m.setNodeView(order: s),
                itemBuilder: (_) => [for (final e in nodeSortNames.entries) CheckedPopupMenuItem(value: e.key, checked: e.key == m.sort, child: Text(e.value))],
              ),
              PopupMenuButton<String>(
                tooltip: 'Вид списка',
                icon: const Icon(Icons.view_agenda_outlined),
                onSelected: (k) {
                  if (k == 'group') _prefs.update(groupByCountry: !_prefs.groupByCountry);
                  if (k == 'compact') _prefs.update(compact: !_prefs.compact);
                  if (k == 'collapse') _prefs.setCollapsed({for (final g in _groupKeys(m.visibleNodes)) g});
                  if (k == 'expand') _prefs.setCollapsed({});
                },
                itemBuilder: (_) => [
                  CheckedPopupMenuItem(value: 'group', checked: _prefs.groupByCountry, child: const Text('Группы по странам')),
                  CheckedPopupMenuItem(value: 'compact', checked: _prefs.compact, child: const Text('Плотный список')),
                  if (_prefs.groupByCountry) ...[
                    const PopupMenuDivider(),
                    const PopupMenuItem(value: 'collapse', child: ListTile(dense: true, leading: Icon(Icons.unfold_less), title: Text('Свернуть все группы'))),
                    const PopupMenuItem(value: 'expand', child: ListTile(dense: true, leading: Icon(Icons.unfold_more), title: Text('Развернуть все группы'))),
                  ],
                ],
              ),
              IconButton(
                tooltip: m.selecting ? 'Выйти из выбора' : 'Выбрать узлы для замера',
                onPressed: m.measure.running ? null : () => m.setSelecting(!m.selecting),
                icon: Icon(m.selecting ? Icons.close : Icons.checklist),
              ),
              IconButton(tooltip: 'Обновить подписки и проверить', onPressed: m.nodesLoading ? null : m.refreshAndCheck, icon: const Icon(Icons.refresh)),
            ]),
          ),
          SizedBox(
            height: 48,
            child: ListView(scrollDirection: Axis.horizontal, padding: const EdgeInsets.symmetric(horizontal: 16), children: [
              _chip('Живые', m.aliveOnly, (v) => m.setNodeView(alive: v)),
              _chip('★ Избранные', m.favOnly, (v) => m.setNodeView(fav: v)),
              _chip('YouTube', m.youtubeOnly, (v) => m.setNodeView(youtube: v)),
              if (m.countries.isNotEmpty)
                Padding(
                  padding: const EdgeInsets.only(right: 8),
                  child: PopupMenuButton<String?>(
                    tooltip: 'Страна выхода',
                    onSelected: m.setCountry,
                    itemBuilder: (_) => [
                      const PopupMenuItem(value: null, child: Text('Все страны')),
                      for (final e in m.countries) PopupMenuItem(value: e.key, child: Text('${flagOf(e.key)}  ${countryName(e.key)} · ${e.value}')),
                    ],
                    child: Chip(
                      avatar: const Icon(Icons.public, size: 18),
                      label: Text(m.countryFilter == null ? 'Страна' : '${flagOf(m.countryFilter!)} ${countryName(m.countryFilter!)}'),
                      backgroundColor: m.countryFilter == null ? null : cs.secondaryContainer,
                    ),
                  ),
                ),
            ]),
          ),
          if (m.measure.running) _measureBar(context, m) else if (m.selecting) _selectBar(context, m),
          _activityBar(context, m),
          if (m.nodesLoading && !m.measure.running && !(m.status?.activity.busy ?? false)) const LinearProgressIndicator(minHeight: 2),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 4),
            child: Align(
              alignment: Alignment.centerLeft,
              child: Text(
                m.aliveOnly ? 'Живых: ${m.nodes.length}${list.length != m.nodes.length ? ' · показано ${list.length}' : ''}${m.status != null ? ' (в базе ${m.status!.nodes})' : ''}' : 'Показано ${list.length} из ${m.nodes.length}',
                style: Theme.of(context).textTheme.labelSmall?.copyWith(color: cs.onSurfaceVariant),
              ),
            ),
          ),
          Expanded(child: _list(context, m, list, activeId)),
          if (!m.nodesLoading && list.isEmpty)
            Padding(
              padding: const EdgeInsets.all(24),
              child: Text(m.nodesError ?? ((m.status?.activity.busy ?? false) ? 'Идёт ${m.status!.activity.refreshing ? 'обновление подписок' : 'проверка узлов'}: живые узлы появятся по мере готовности.' : (m.aliveOnly ? 'Живых узлов пока нет. Нажмите ⟳.' : 'Ничего не найдено')), textAlign: TextAlign.center),
            ),
        ]);
      },
    );
  }

  Widget _list(BuildContext context, AppModel m, List<NodeDto> list, String activeId) {
    final compact = _prefs.compact;
    Widget tile(NodeDto n) => NodeTile(key: ValueKey(n.id), model: m, node: n, active: n.id == activeId, selecting: m.selecting, checked: m.selected.contains(n.id), compact: compact);
    final warp = _warpTile(context, m, activeId == 'warp');
    if (!_prefs.groupByCountry) {
      return ListView.builder(itemCount: list.length + 1, itemBuilder: (context, i) => i == 0 ? warp : tile(list[i - 1]));
    }
    final groups = _groupNodes(list);
    final keys = groups.keys.toList();
    final cs = Theme.of(context).colorScheme;
    final searching = m.search.trim().isNotEmpty; // при поиске свёрнутость не скрывает найденное
    return CustomScrollView(slivers: [
      SliverToBoxAdapter(child: warp),
      for (final k in keys)
        SliverMainAxisGroup(slivers: [
          SliverPersistentHeader(
            pinned: true,
            delegate: _GroupHeader(
              title: k == '#own' ? 'Мои серверы' : (k == '#none' ? 'Страна не определена' : countryName(k)),
              flag: k == '#own' ? '⭐' : (k == '#none' ? '🌐' : flagOf(k)),
              count: groups[k]!.length,
              color: cs.surface,
              textColor: cs.onSurfaceVariant,
              collapsed: !searching && _prefs.collapsed.contains(k),
              hasActive: groups[k]!.any((n) => n.id == activeId),
              accent: cs.primary,
              onTap: searching ? null : () => _prefs.toggleGroup(k),
            ),
          ),
          if (searching || !_prefs.collapsed.contains(k)) SliverList.builder(itemCount: groups[k]!.length, itemBuilder: (context, i) => tile(groups[k]![i])),
        ]),
    ]);
  }

  /// Группы списка: свои серверы, затем страны по числу узлов, без данных — в конце.
  static Map<String, List<NodeDto>> _groupNodes(List<NodeDto> list) {
    final groups = <String, List<NodeDto>>{};
    for (final n in list) {
      final key = n.own ? '#own' : (n.exit?.country.isNotEmpty == true ? n.exit!.country : '#none');
      groups.putIfAbsent(key, () => []).add(n);
    }
    int rank(String k) => k == '#own' ? 0 : (k == '#none' ? 2 : 1);
    final keys = groups.keys.toList()
      ..sort((a, b) {
        final r = rank(a).compareTo(rank(b));
        if (r != 0) return r;
        final c = groups[b]!.length.compareTo(groups[a]!.length);
        return c != 0 ? c : a.compareTo(b);
      });
    return {for (final k in keys) k: groups[k]!};
  }

  static Iterable<String> _groupKeys(List<NodeDto> list) => _groupNodes(list).keys;

  Widget _selectBar(BuildContext context, AppModel m) {
    final cs = Theme.of(context).colorScheme;
    return Container(
      color: cs.secondaryContainer,
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 4),
      child: Row(children: [
        Expanded(child: Text('Выбрано: ${m.selected.length}', style: TextStyle(color: cs.onSecondaryContainer))),
        TextButton(onPressed: () => m.selectAllVisible(), child: const Text('Все показанные')),
        TextButton(onPressed: m.selected.isEmpty ? null : m.clearSelection, child: const Text('Сбросить')),
        PopupMenuButton<String>(
          tooltip: 'Что замерить',
          enabled: m.selected.isNotEmpty,
          onSelected: (k) => m.startMeasure(ping: k == 'ping' || k == 'all', exit: k == 'exit' || k == 'all', speed: k == 'speed' || k == 'all'),
          itemBuilder: (_) => const [
            PopupMenuItem(value: 'all', child: Text('Всё: пинг, страна, чистота, скорость')),
            PopupMenuItem(value: 'ping', child: Text('Пинг')),
            PopupMenuItem(value: 'exit', child: Text('Страна и чистота IP')),
            PopupMenuItem(value: 'speed', child: Text('Скорость')),
          ],
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
            child: Row(mainAxisSize: MainAxisSize.min, children: [
              Icon(Icons.speed, size: 18, color: m.selected.isEmpty ? cs.outline : cs.primary),
              const SizedBox(width: 4),
              Text('Замерить', style: TextStyle(color: m.selected.isEmpty ? cs.outline : cs.primary)),
            ]),
          ),
        ),
      ]),
    );
  }

  Widget _measureBar(BuildContext context, AppModel m) {
    final cs = Theme.of(context).colorScheme;
    final ms = m.measure;
    return Container(
      color: cs.secondaryContainer,
      padding: const EdgeInsets.fromLTRB(16, 6, 8, 6),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Expanded(child: Text('Замер: ${ms.stageName} · ${ms.done} из ${ms.total}', style: TextStyle(color: cs.onSecondaryContainer))),
          TextButton(onPressed: m.cancelMeasure, child: const Text('Остановить')),
        ]),
        LinearProgressIndicator(value: ms.fraction),
      ]),
    );
  }

  /// Что делает демон: обновление подписок или круг проверки узлов (с прогрессом); пусто, если ничего не идёт.
  Widget _activityBar(BuildContext context, AppModel m) {
    final a = m.status?.activity;
    if (a == null || !a.busy) return const SizedBox.shrink();
    final cs = Theme.of(context).colorScheme;
    final String text;
    final double? value;
    if (a.refreshing) {
      text = 'Обновляю подписки…';
      value = null;
    } else {
      text = 'Проверяю узлы: ${a.checkDone} из ${a.checkPicked} · живых ${a.checkOk}';
      value = a.checkPicked > 0 ? a.checkDone / a.checkPicked : null;
    }
    return Container(
      key: const Key('node-activity'),
      color: cs.secondaryContainer,
      padding: const EdgeInsets.fromLTRB(16, 6, 16, 6),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text(text, style: TextStyle(color: cs.onSecondaryContainer, fontSize: 13)),
        const SizedBox(height: 4),
        LinearProgressIndicator(value: value, minHeight: 3),
      ]),
    );
  }

  Widget _chip(String label, bool on, ValueChanged<bool> f) => Padding(padding: const EdgeInsets.only(right: 8), child: FilterChip(label: Text(label), selected: on, onSelected: f));

  Widget _warpTile(BuildContext context, AppModel m, bool active) {
    final cs = Theme.of(context).colorScheme;
    return ListTile(
      leading: Container(width: 44, height: 44, decoration: BoxDecoration(shape: BoxShape.circle, color: cs.surfaceContainerHighest), child: Icon(Icons.cloud_outlined, color: active ? cs.primary : null)),
      title: const Text('Cloudflare WARP'),
      subtitle: const Text('Встроенный выход'),
      selected: active,
      trailing: m.nodeBusy == 'warp'
          ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2))
          : (active ? Icon(Icons.check_circle, color: cs.primary) : OutlinedButton(onPressed: m.connectWarp, child: const Text('Выбрать'))),
    );
  }
}

/// Липкий заголовок группы (страна): по нажатию сворачивает и разворачивает группу.
class _GroupHeader extends SliverPersistentHeaderDelegate {
  _GroupHeader({
    required this.title,
    required this.flag,
    required this.count,
    required this.color,
    required this.textColor,
    required this.collapsed,
    required this.hasActive,
    required this.accent,
    this.onTap,
  });

  final String title;
  final String flag;
  final int count;
  final Color color;
  final Color textColor;
  final bool collapsed;
  final bool hasActive; // в группе активный узел — заметно и в свёрнутом виде
  final Color accent;
  final VoidCallback? onTap;

  static const _height = 38.0;

  @override
  double get minExtent => _height;
  @override
  double get maxExtent => _height;

  @override
  Widget build(BuildContext context, double shrinkOffset, bool overlapsContent) => Material(
        color: color.withValues(alpha: 0.96),
        child: InkWell(
          onTap: onTap,
          // липкому заголовку нужна определённая высота: ребёнок получает свободные ограничения
          child: SizedBox(
            height: _height,
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12),
              child: Row(children: [
                if (onTap != null) Icon(collapsed ? Icons.chevron_right : Icons.expand_more, size: 20, color: textColor) else const SizedBox(width: 20),
                const SizedBox(width: 4),
                Text(flag, style: const TextStyle(fontSize: 16)),
                const SizedBox(width: 8),
                Text(title, style: TextStyle(fontSize: 13, fontWeight: FontWeight.w600, color: textColor)),
                const SizedBox(width: 6),
                Text('· $count', style: TextStyle(fontSize: 12, color: textColor.withValues(alpha: 0.7))),
                if (hasActive && collapsed) ...[const SizedBox(width: 8), Icon(Icons.check_circle, size: 15, color: accent)],
              ]),
            ),
          ),
        ),
      );

  @override
  bool shouldRebuild(_GroupHeader old) =>
      old.title != title || old.count != count || old.color != color || old.collapsed != collapsed || old.hasActive != hasActive || (old.onTap == null) != (onTap == null);
}

/// Строка узла.
class NodeTile extends StatelessWidget {
  const NodeTile({super.key, required this.model, required this.node, required this.active, this.selecting = false, this.checked = false, this.compact = false});

  final AppModel model;
  final NodeDto node;
  final bool active;
  final bool selecting;
  final bool checked;
  final bool compact;

  static String ago(DateTime? t) {
    if (t == null) return 'не проверен';
    final d = DateTime.now().difference(t.toLocal());
    if (d.inSeconds < 90) return 'проверен только что';
    if (d.inMinutes < 60) return 'проверен ${d.inMinutes} мин назад';
    if (d.inHours < 24) return 'проверен ${d.inHours} ч назад';
    return 'проверен ${d.inDays} дн назад';
  }

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final busy = model.nodeBusy == node.id;
    final line = node.isChain
        ? 'цепочка из ${node.chain.length}${node.label.isNotEmpty ? ' · ${node.address}' : ''} · ${ago(node.l2At)}'
        : '${node.proto}${node.label.isNotEmpty ? ' · ${node.address}' : ''} · ${ago(node.l2At)}';
    return ListTile(
      selected: active,
      dense: compact,
      visualDensity: compact ? VisualDensity.compact : VisualDensity.standard,
      leading: SizedBox(
        width: selecting ? 76 : 44,
        child: Row(children: [
          if (selecting) Checkbox(value: checked, onChanged: (_) => model.toggleSelect(node.id), visualDensity: VisualDensity.compact),
          FlagBadge(country: node.exit?.country, size: compact ? 34 : 40),
        ]),
      ),
      title: Text(node.title, overflow: TextOverflow.ellipsis),
      subtitle: compact
          ? Text(line, overflow: TextOverflow.ellipsis, style: const TextStyle(fontSize: 12))
          : Column(crossAxisAlignment: CrossAxisAlignment.start, children: [Text(line, overflow: TextOverflow.ellipsis), const SizedBox(height: 4), NodePills(node: node)]),
      isThreeLine: !compact,
      onTap: selecting ? () => model.toggleSelect(node.id) : (busy || active ? null : () => model.connectNode(node)),
      onLongPress: selecting
          ? null
          : () {
              model.setSelecting(true);
              model.toggleSelect(node.id);
            },
      trailing: Row(mainAxisSize: MainAxisSize.min, children: [
        if (busy) const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2)) else if (active) Icon(Icons.check_circle, color: cs.primary),
        IconButton(
          tooltip: node.favorite ? 'Убрать из избранного' : 'Добавить в избранное',
          icon: Icon(node.favorite ? Icons.star : Icons.star_border, color: node.favorite ? Colors.amber : null),
          onPressed: () => model.toggleFavorite(node),
        ),
        PopupMenuButton<String>(
          tooltip: 'Действия',
          icon: const Icon(Icons.more_vert),
          onSelected: (k) async {
            switch (k) {
              case 'rename':
                await renameNodeDialog(context, model, node);
              case 'edit':
                await editOwnDialog(context, model, node);
              case 'share':
                await model.shareNode(node);
              case 'qr':
                try {
                  final link = await model.api.nodeLink(node.id);
                  if (context.mounted) await showQrDialog(context, title: 'QR-код сервера', link: link, secret: true);
                } catch (e) {
                  model.toast = 'Не удалось получить ссылку: $e';
                }
              case 'chaincheck':
                await chainCheckDialog(context, model, node);
              case 'ping':
                await model.startMeasure(ping: true, ids: [node.id]);
              case 'speed':
                await model.measureSpeed(node);
              case 'calls':
                await model.probeCalls(node);
              case 'exit':
                await model.detectExit(node);
              case 'delete':
                await model.removeOwn(node);
            }
          },
          itemBuilder: (_) => [
            const PopupMenuItem(value: 'rename', child: ListTile(dense: true, leading: Icon(Icons.drive_file_rename_outline), title: Text('Переименовать'))),
            if (node.own && !node.isChain) const PopupMenuItem(value: 'edit', child: ListTile(dense: true, leading: Icon(Icons.edit_outlined), title: Text('Изменить сервер'))),
            if (!node.isChain) const PopupMenuItem(value: 'qr', child: ListTile(dense: true, leading: Icon(Icons.qr_code_2), title: Text('Показать QR-код'))),
            if (!node.isChain) const PopupMenuItem(value: 'share', child: ListTile(dense: true, leading: Icon(Icons.share_outlined), title: Text('Поделиться ссылкой'))),
            if (node.isChain) const PopupMenuItem(value: 'chaincheck', child: ListTile(dense: true, leading: Icon(Icons.rule), title: Text('Проверить звенья'))),
            const PopupMenuItem(value: 'ping', child: ListTile(dense: true, leading: Icon(Icons.network_ping), title: Text('Проверить пинг'))),
            const PopupMenuItem(value: 'speed', child: ListTile(dense: true, leading: Icon(Icons.speed), title: Text('Замерить скорость'))),
            const PopupMenuItem(value: 'calls', child: ListTile(dense: true, leading: Icon(Icons.headset_mic_outlined), title: Text('Проверить для звонков'))),
            const PopupMenuItem(value: 'exit', child: ListTile(dense: true, leading: Icon(Icons.travel_explore), title: Text('Проверить страну и чистоту IP'))),
            if (node.own) const PopupMenuItem(value: 'delete', child: ListTile(dense: true, leading: Icon(Icons.delete_outline), title: Text('Удалить'))),
          ],
        ),
      ]),
    );
  }
}
