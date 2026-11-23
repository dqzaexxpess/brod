import 'package:flutter/material.dart';

import 'api.dart';
import 'model.dart';

/// Переименовать любой узел (пустое название — вернуть адрес).
Future<void> renameNodeDialog(BuildContext context, AppModel model, NodeDto n) async {
  final c = TextEditingController(text: n.label);
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: const Text('Название узла'),
      content: TextField(controller: c, autofocus: true, maxLength: 60, decoration: InputDecoration(hintText: n.address, helperText: 'Пусто — показывать адрес')),
      actions: [
        TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
        FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Сохранить')),
      ],
    ),
  );
  if (ok == true) await model.setNodeLabel(n, c.text.trim());
}

/// Правка собственного сервера: название, адрес, порт или замена ссылкой целиком.
Future<void> editOwnDialog(BuildContext context, AppModel model, NodeDto n) async {
  final label = TextEditingController(text: n.label);
  final server = TextEditingController(text: n.server);
  final port = TextEditingController(text: n.port > 0 ? '${n.port}' : '');
  final link = TextEditingController();
  String? err;
  await showDialog<void>(
    context: context,
    builder: (ctx) => StatefulBuilder(
      builder: (ctx, setS) => AlertDialog(
        title: const Text('Изменить сервер'),
        content: SizedBox(
          width: 460,
          child: SingleChildScrollView(
            child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
              TextField(controller: label, maxLength: 60, decoration: const InputDecoration(labelText: 'Название')),
              const SizedBox(height: 8),
              Row(children: [
                Expanded(flex: 3, child: TextField(controller: server, decoration: const InputDecoration(labelText: 'Адрес'))),
                const SizedBox(width: 12),
                Expanded(child: TextField(controller: port, keyboardType: TextInputType.number, decoration: const InputDecoration(labelText: 'Порт'))),
              ]),
              const SizedBox(height: 14),
              ExpansionTile(
                tilePadding: EdgeInsets.zero,
                title: const Text('Заменить ссылкой'),
                subtitle: const Text('Чтобы поменять uuid, пароль, SNI и т. п.'),
                children: [TextField(controller: link, minLines: 2, maxLines: 5, decoration: const InputDecoration(hintText: 'vless://… или ss://…'))],
              ),
              if (err != null) Padding(padding: const EdgeInsets.only(top: 8), child: Text(err!, style: TextStyle(color: Theme.of(ctx).colorScheme.error))),
            ]),
          ),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Отмена')),
          FilledButton(
            onPressed: () async {
              final p = int.tryParse(port.text.trim());
              final l = link.text.trim();
              final ok = await model.editOwn(
                n,
                label: label.text.trim() == n.label ? null : label.text.trim(),
                server: l.isEmpty && server.text.trim() != n.server ? server.text.trim() : null,
                port: l.isEmpty && p != null && p != n.port ? p : null,
                link: l.isEmpty ? null : l,
              );
              if (ok) {
                if (ctx.mounted) Navigator.pop(ctx);
              } else {
                setS(() => err = model.toast);
              }
            },
            child: const Text('Сохранить'),
          ),
        ],
      ),
    ),
  );
}

/// Звенья для цепочки: сначала свои серверы, затем остальные узлы (живые первыми). Сами цепочки не берутся; WireGuard берётся, но только первым звеном.
List<NodeDto> chainCandidates(AppModel model) {
  final seen = <String>{};
  final out = <NodeDto>[];
  bool usable(NodeDto n) => !n.isChain && n.proto != 'warp' && seen.add(n.id);
  for (final n in model.ownNodes) {
    if (usable(n)) out.add(n);
  }
  final rest = [for (final n in model.nodes) if (!seen.contains(n.id)) n]..sort((a, b) => (b.alive ? 1 : 0).compareTo(a.alive ? 1 : 0));
  for (final n in rest) {
    if (usable(n)) out.add(n);
  }
  return out;
}

/// Собрать цепочку прокси: выбрать 2–3 узла по порядку (первое звено, затем следующие, последнее — выход) и, если нужно, дать название.
Future<void> chainDialog(BuildContext context, AppModel model) async {
  final name = TextEditingController();
  final chosen = <NodeDto>[];
  final search = TextEditingController();
  final all = chainCandidates(model);
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => StatefulBuilder(builder: (ctx, setState) {
      final q = search.text.trim().toLowerCase();
      final shown = [for (final n in all) if (q.isEmpty || n.title.toLowerCase().contains(q) || n.proto.toLowerCase().contains(q)) n].take(60).toList();
      final cs = Theme.of(ctx).colorScheme;
      return AlertDialog(
        title: const Text('Собрать цепочку'),
        content: SizedBox(
          width: 460,
          height: 480,
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(
              'Трафик пойдёт через звенья по порядку: сначала первое, последним выход. Двух звеньев обычно достаточно: каждое следующее прибавляет задержку и риск, что цепочка не заработает (максимум три). WireGuard можно поставить только первым звеном.',
              style: Theme.of(ctx).textTheme.bodySmall,
            ),
            const SizedBox(height: 10),
            if (chosen.isEmpty)
              Text('Звенья не выбраны', style: TextStyle(color: cs.onSurfaceVariant))
            else
              Wrap(spacing: 6, runSpacing: 4, crossAxisAlignment: WrapCrossAlignment.center, children: [
                for (var i = 0; i < chosen.length; i++) ...[
                  if (i > 0) const Icon(Icons.arrow_forward, size: 16),
                  InputChip(
                    label: Text('${i + 1}. ${chosen[i].title}', overflow: TextOverflow.ellipsis),
                    onDeleted: () => setState(() => chosen.removeAt(i)),
                  ),
                ],
              ]),
            const SizedBox(height: 10),
            TextField(controller: name, maxLength: 60, decoration: const InputDecoration(labelText: 'Название (необязательно)', isDense: true)),
            TextField(controller: search, onChanged: (_) => setState(() {}), decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: 'Найти узел', isDense: true)),
            Expanded(
              child: shown.isEmpty
                  ? const Center(child: Text('Подходящих узлов нет'))
                  : ListView(children: [
                      for (final n in shown)
                        ListTile(
                          dense: true,
                          enabled: chosen.length < 3 && !chosen.any((c) => c.id == n.id) && (n.proto != 'wireguard' || chosen.isEmpty),
                          title: Text(n.title, overflow: TextOverflow.ellipsis),
                          subtitle: Text('${n.proto}${n.proto == 'wireguard' ? ' (только первым звеном)' : ''}${n.own ? ' · свой' : ''}${n.alive ? ' · живой' : ''}'),
                          onTap: () => setState(() => chosen.add(n)),
                        ),
                    ]),
            ),
          ]),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
          FilledButton(onPressed: chosen.length >= 2 ? () => Navigator.pop(ctx, true) : null, child: const Text('Создать')),
        ],
      );
    }),
  );
  if (ok == true) await model.addChain(name.text.trim(), [for (final n in chosen) n.id]);
}

/// Проверка звеньев цепочки: какое звено не отвечает. При сбое предлагает собрать цепочку заново из других узлов.
Future<void> chainCheckDialog(BuildContext context, AppModel model, NodeDto chain) async {
  late final Future<List<HopCheckDto>> res;
  res = model.api.checkChain(chain.id);
  await showDialog<void>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: const Text('Звенья цепочки'),
      content: SizedBox(
        width: 360,
        child: FutureBuilder<List<HopCheckDto>>(
          future: res,
          builder: (ctx, snap) {
            if (snap.hasError) return Text('Не удалось проверить: ${snap.error}');
            final hops = snap.data;
            if (hops == null) return const Row(children: [SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2)), SizedBox(width: 12), Text('Проверяю…')]);
            final cs = Theme.of(ctx).colorScheme;
            final dead = [for (var i = 0; i < hops.length; i++) if (hops[i].state == 'fail') i + 1];
            return Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
              for (var i = 0; i < hops.length; i++)
                ListTile(
                  dense: true,
                  contentPadding: EdgeInsets.zero,
                  leading: switch (hops[i].state) {
                    'ok' => Icon(Icons.check_circle_outline, color: cs.primary),
                    'fail' => Icon(Icons.error_outline, color: cs.error),
                    _ => Icon(Icons.help_outline, color: cs.onSurfaceVariant),
                  },
                  title: Text('${i + 1}. ${hops[i].server}:${hops[i].port}', overflow: TextOverflow.ellipsis),
                  subtitle: Text(switch (hops[i].state) {
                    'ok' => '${hops[i].proto} · отвечает${hops[i].ms > 0 ? ' · ${hops[i].ms} мс' : ''}',
                    'fail' => '${hops[i].proto} · не отвечает${hops[i].detail.isEmpty ? '' : ': ${hops[i].detail}'}',
                    _ => '${hops[i].proto} · на UDP, напрямую не проверяется',
                  }),
                ),
              const SizedBox(height: 8),
              Text(
                dead.isEmpty
                    ? 'Серверы звеньев отвечают. Если цепочка всё равно не работает, причина может быть в самих звеньях (не принимают подключение от предыдущего).'
                    : 'Не отвечает ${dead.length == 1 ? 'звено ${dead.first}' : 'звенья ${dead.join(', ')}'}. Замените ${dead.length == 1 ? 'его' : 'их'}: соберите цепочку заново из других узлов.',
                style: TextStyle(fontSize: 12, color: dead.isEmpty ? cs.onSurfaceVariant : cs.error),
              ),
            ]);
          },
        ),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Закрыть')),
        TextButton(
          onPressed: () {
            Navigator.pop(ctx);
            chainDialog(context, model);
          },
          child: const Text('Собрать заново'),
        ),
      ],
    ),
  );
}
