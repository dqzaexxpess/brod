import 'package:flutter/material.dart';

import 'model.dart';
import 'nodes_page.dart';

/// Свои серверы (свой VPS и т. п.): добавление ссылкой или конфигом, список, выбор, удаление. Остальные операции (замеры, избранное) — как у обычных узлов.
class OwnPage extends StatefulWidget {
  const OwnPage({super.key, required this.model});

  final AppModel model;

  @override
  State<OwnPage> createState() => _OwnPageState();
}

class _OwnPageState extends State<OwnPage> {
  @override
  void initState() {
    super.initState();
    Future.microtask(widget.model.loadOwn);
  }

  Future<void> _add() async {
    final c = TextEditingController();
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Добавить свой сервер'),
        content: SizedBox(
          width: 460,
          child: TextField(
            controller: c,
            minLines: 4,
            maxLines: 10,
            decoration: const InputDecoration(
              hintText: 'vless://, ss://, trojan://… (по одной в строке)\nили конфиг sing-box / Clash / Xray\nили файл WireGuard / AmneziaWG',
              border: OutlineInputBorder(),
            ),
          ),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Добавить')),
        ],
      ),
    );
    if (ok == true && c.text.trim().isNotEmpty) {
      final m = widget.model;
      await m.addOwn(c.text);
    }
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final cs = Theme.of(context).colorScheme;
        final activeId = m.status?.active?.id ?? '';
        return Scaffold(
          appBar: AppBar(title: const Text('Мои серверы')),
          floatingActionButton: FloatingActionButton.extended(onPressed: _add, icon: const Icon(Icons.add), label: const Text('Добавить сервер')),
          body: Column(children: [
            SizedBox(height: 2, child: (m.ownLoading) ? const LinearProgressIndicator(minHeight: 2) : null),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
              child: Text(
                'Свои серверы всегда наверху и не удаляются при очистке',
                style: Theme.of(context).textTheme.bodySmall?.copyWith(color: cs.onSurfaceVariant),
              ),
            ),
            Expanded(
              child: m.ownNodes.isEmpty && !m.ownLoading
                  ? const Center(child: Padding(padding: EdgeInsets.all(24), child: Text('Своих серверов пока нет.\nНажмите «Добавить сервер» и вставьте ссылку.', textAlign: TextAlign.center)))
                  : ListView(padding: const EdgeInsets.only(bottom: 88), children: [
                      for (final n in m.ownNodes)
                        NodeTile(
                          key: ValueKey(n.id),
                          model: m,
                          node: n,
                          active: n.id == activeId,
                        ),
                    ]),
            ),
          ]),
        );
      },
    );
  }
}
