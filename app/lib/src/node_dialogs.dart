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
