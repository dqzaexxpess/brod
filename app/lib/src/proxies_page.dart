import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'api.dart';
import 'model.dart';
import 'nodes_page.dart' show NodeTile;
import 'widgets.dart';

/// Вкладка «Прокси»: прокси для Telegram (MTProto, SOCKS5) из источников и свои; проверка настоящим обменом с датацентром,
/// открытие в Telegram одним нажатием.
class ProxiesPage extends StatefulWidget {
  const ProxiesPage({super.key, required this.model, required this.visible});

  final AppModel model;
  final bool visible;

  @override
  State<ProxiesPage> createState() => _ProxiesPageState();
}

class _ProxiesPageState extends State<ProxiesPage> {
  Timer? _timer;

  @override
  void initState() {
    super.initState();
    if (widget.visible) _activate();
  }

  @override
  void didUpdateWidget(ProxiesPage old) {
    super.didUpdateWidget(old);
    if (widget.visible && !old.visible) _activate();
    if (!widget.visible) _timer?.cancel();
  }

  void _activate() {
    scheduleMicrotask(widget.model.loadTgProxies); // не во время сборки: загрузка сообщает слушателям синхронно
    _timer?.cancel();
    var tick = 0;
    _timer = Timer.periodic(const Duration(seconds: 3), (_) {
      if (!widget.model.foreground) return; // приложение не на экране
      tick++;
      final a = widget.model.status?.activity;
      if ((a?.tgChecking ?? false) || tick % 7 == 0) widget.model.loadTgProxies(quiet: true); // во время проверки — чаще
    });
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _add(BuildContext context) async {
    final c = TextEditingController();
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Добавить прокси'),
        content: SizedBox(
          width: 460,
          child: TextField(
            controller: c,
            autofocus: true,
            minLines: 3,
            maxLines: 8,
            decoration: const InputDecoration(hintText: 'tg://proxy?server=…&port=…&secret=…\nhttps://t.me/proxy?…\ntg://socks?…\n(можно несколько, по одной в строке)'),
          ),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Добавить')),
        ],
      ),
    );
    if (ok == true && c.text.trim().isNotEmpty) await widget.model.tgAddText(c.text);
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final cs = Theme.of(context).colorScheme;
        final list = m.tgProxies;
        final best = m.tgBest;
        return Column(children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 12, 8, 0),
            child: Row(children: [
              Expanded(child: Text('Прокси для Telegram', style: Theme.of(context).textTheme.titleMedium)),
              IconButton(tooltip: 'Добавить свой прокси', onPressed: () => _add(context), icon: const Icon(Icons.add)),
              IconButton(tooltip: 'Проверить сейчас', onPressed: m.tgLoading ? null : m.tgCheckNow, icon: const Icon(Icons.refresh)),
            ]),
          ),
          SizedBox(
            height: 48,
            child: ListView(scrollDirection: Axis.horizontal, padding: const EdgeInsets.symmetric(horizontal: 16), children: [
              _chip('Живые', m.tgAliveOnly, (v) => m.setTgView(alive: v)),
              _chip('★ Избранные', m.tgFavOnly, (v) => m.setTgView(fav: v)),
              _chip('MTProto', m.tgKind == 'mtproto', (v) => m.setTgView(kind: v ? 'mtproto' : '')),
              _chip('SOCKS5', m.tgKind == 'socks5', (v) => m.setTgView(kind: v ? 'socks5' : '')),
              _chip('WEB', m.tgKind == 'webproxy', (v) => m.setTgView(kind: v ? 'webproxy' : '')),
            ]),
          ),
          _activityBar(context, m),
          if (m.tgLoading && !(m.status?.activity.tgChecking ?? false)) const LinearProgressIndicator(minHeight: 2),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 4),
            child: Row(children: [
              Expanded(
                child: Text('Живых: ${m.tgAlive} из ${m.tgTotal}${list.length != m.tgTotal ? ' · показано ${list.length}' : ''}',
                    style: Theme.of(context).textTheme.labelSmall?.copyWith(color: cs.onSurfaceVariant)),
              ),
              if (best != null) FilledButton.tonalIcon(onPressed: () => m.openTg(best), icon: const Icon(Icons.bolt, size: 18), label: Text('Лучший · ${best.latencyMs} мс')),
            ]),
          ),
          Expanded(
            child: list.isEmpty
                ? Center(
                    child: Padding(
                      padding: const EdgeInsets.all(24),
                      child: Text(
                        m.tgError ?? (m.tgLoading ? 'Загружаю…' : (m.tgAliveOnly ? 'Живых прокси пока нет. Нажмите ⟳.' : 'Прокси нет. Они придут с обновлением подписок или добавьте свой (＋).')),
                        textAlign: TextAlign.center,
                      ),
                    ),
                  )
                : ListView.builder(itemCount: list.length, itemBuilder: (context, i) => _tile(context, m, list[i])),
          ),
        ]);
      },
    );
  }

  /// Идёт круг проверки прокси (в том числе плановый): сколько проверено и сколько живых.
  Widget _activityBar(BuildContext context, AppModel m) {
    final a = m.status?.activity;
    if (a == null || !a.tgChecking) return const SizedBox.shrink();
    final cs = Theme.of(context).colorScheme;
    return Container(
      key: const Key('tg-activity'),
      color: cs.secondaryContainer,
      padding: const EdgeInsets.fromLTRB(16, 6, 16, 6),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text('Проверяю прокси: ${a.tgDone} из ${a.tgPicked} · живых ${a.tgOk}', style: TextStyle(color: cs.onSecondaryContainer, fontSize: 13)),
        const SizedBox(height: 4),
        LinearProgressIndicator(value: a.tgPicked > 0 ? a.tgDone / a.tgPicked : null, minHeight: 3),
      ]),
    );
  }

  Widget _chip(String label, bool on, ValueChanged<bool> f) => Padding(padding: const EdgeInsets.only(right: 8), child: FilterChip(label: Text(label), selected: on, onSelected: f));

  Widget _tile(BuildContext context, AppModel m, TgProxyDto p) {
    final cs = Theme.of(context).colorScheme;
    final Color color = !p.checked ? cs.outline : (p.ok ? const Color(0xFF2EA66B) : cs.error);
    final String status = !p.checked ? 'не проверен' : (p.ok ? '${p.latencyMs} мс' : 'не отвечает');
    return ListTile(
      key: ValueKey(p.id),
      leading: Container(
        width: 40,
        height: 40,
        decoration: BoxDecoration(shape: BoxShape.circle, color: color.withValues(alpha: 0.16)),
        child: Icon(p.kind == 'socks5' ? Icons.lan_outlined : (p.kind == 'webproxy' ? Icons.language : Icons.send), size: 20, color: color),
      ),
      title: Text('${p.server}:${p.port}', overflow: TextOverflow.ellipsis),
      subtitle: Text('${p.kindLabel} · $status${p.checked ? ' · ${NodeTile.ago(p.checkedAt)}' : ''}${p.own ? ' · свой' : ''}', overflow: TextOverflow.ellipsis),
      onTap: () => m.openTg(p),
      trailing: Row(mainAxisSize: MainAxisSize.min, children: [
        IconButton(
          tooltip: p.favorite ? 'Убрать из избранного' : 'Добавить в избранное',
          icon: Icon(p.favorite ? Icons.star : Icons.star_border, color: p.favorite ? Colors.amber : null),
          onPressed: () => m.tgToggleFav(p),
        ),
        PopupMenuButton<String>(
          tooltip: 'Действия',
          icon: const Icon(Icons.more_vert),
          onSelected: (k) async {
            switch (k) {
              case 'open':
                await m.openTg(p);
              case 'copy':
                await Clipboard.setData(ClipboardData(text: p.link));
                if (context.mounted) showToast(context, 'Ссылка скопирована');
              case 'delete':
                await m.tgRemove(p);
            }
          },
          itemBuilder: (_) => [
            const PopupMenuItem(value: 'open', child: ListTile(dense: true, leading: Icon(Icons.send), title: Text('Открыть в Telegram'))),
            const PopupMenuItem(value: 'copy', child: ListTile(dense: true, leading: Icon(Icons.copy), title: Text('Скопировать ссылку'))),
            if (p.own) const PopupMenuItem(value: 'delete', child: ListTile(dense: true, leading: Icon(Icons.delete_outline), title: Text('Удалить'))),
          ],
        ),
      ]),
    );
  }
}
