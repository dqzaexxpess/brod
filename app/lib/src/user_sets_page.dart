import 'package:flutter/material.dart';

import 'format.dart';
import 'model.dart';

/// Свои списки маршрутизации по URL: адрес rule-set (бинарный `.srs` или исходный JSON sing-box). Ядро скачивает и проверяет список сразу,
/// обновляет раз в 12 часов (напрямую, при сбое через активный узел), а в правилах он выбирается так же, как встроенные.
class UserSetsPage extends StatefulWidget {
  const UserSetsPage({super.key, required this.model});

  final AppModel model;

  @override
  State<UserSetsPage> createState() => _UserSetsPageState();
}

class _UserSetsPageState extends State<UserSetsPage> {
  @override
  void initState() {
    super.initState();
    Future.microtask(widget.model.loadUserSets);
  }

  Future<void> _add() async {
    final url = TextEditingController();
    final title = TextEditingController();
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Добавить список'),
        content: Column(mainAxisSize: MainAxisSize.min, children: [
          TextField(controller: url, autofocus: true, decoration: const InputDecoration(labelText: 'Адрес (https)', hintText: 'https://…/list.srs или .json')),
          const SizedBox(height: 16), // подпись соседнего поля при фокусе заходит на рамку верхнего
          TextField(controller: title, decoration: const InputDecoration(labelText: 'Название (необязательно)')),
          const SizedBox(height: 8),
          Text('Список не подписан: добавляйте только те, которым доверяете.',
              style: Theme.of(ctx).textTheme.bodySmall),
        ]),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Добавить')),
        ],
      ),
    );
    if (ok == true && url.text.trim().isNotEmpty) await widget.model.addUserSet(url.text.trim(), title.text.trim());
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final cs = Theme.of(context).colorScheme;
        return Scaffold(
          appBar: AppBar(title: const Text('Свои списки'), actions: [
            IconButton(tooltip: 'Обновить сейчас', icon: const Icon(Icons.refresh), onPressed: m.updateRuleSets),
          ]),
          floatingActionButton: FloatingActionButton.extended(onPressed: m.userSetsBusy ? null : _add, icon: const Icon(Icons.add), label: const Text('Добавить список')),
          body: Column(children: [
            if (m.userSetsBusy) const LinearProgressIndicator(minHeight: 2),
            if (m.toast != null) Padding(padding: const EdgeInsets.all(12), child: Text(m.toast!, style: TextStyle(color: cs.error))),
            Expanded(
              child: m.userSets.isEmpty
                  ? const Center(child: Padding(padding: EdgeInsets.all(24), child: Text('Своих списков нет. Добавьте rule-set по адресу: он появится в условиях правил.', textAlign: TextAlign.center)))
                  : ListView(padding: const EdgeInsets.only(bottom: 88), children: [
                      for (final s in m.userSets)
                        ListTile(
                          leading: Icon(s.error.isEmpty ? Icons.list_alt : Icons.warning_amber, color: s.error.isEmpty ? null : cs.error),
                          title: Text(s.label),
                          subtitle: Text([
                            s.url,
                            if (s.updated != null) 'обновлён ${fmtAge(s.updated!)} назад · ${fmtBytes(s.size)}',
                            if (s.error.isNotEmpty) 'не обновился: ${s.error}',
                          ].join('\n')),
                          isThreeLine: true,
                          trailing: IconButton(
                            tooltip: 'Удалить',
                            icon: const Icon(Icons.delete_outline),
                            onPressed: () => m.removeUserSet(s.id),
                          ),
                        ),
                    ]),
            ),
          ]),
        );
      },
    );
  }
}
