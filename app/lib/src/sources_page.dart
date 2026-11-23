import 'package:flutter/material.dart';

import 'api.dart';
import 'candidates_page.dart';
import 'format.dart';
import 'model.dart';

/// Источники подписок: список с состоянием, вкл/выкл, добавление, удаление, обновление сейчас.
class SourcesPage extends StatefulWidget {
  const SourcesPage({super.key, required this.model});

  final AppModel model;

  @override
  State<SourcesPage> createState() => _SourcesPageState();
}

class _SourcesPageState extends State<SourcesPage> {
  // обновление подписок, запущенное с этой страницы: полоска показывается только ему; обновление, которое идёт с вкладки «Узлы», здесь не
  // видно (там у него своя плашка и полоска)
  bool _refreshing = false;

  Future<void> _refresh() async {
    setState(() => _refreshing = true);
    try {
      await widget.model.refreshAndCheck();
    } finally {
      if (mounted) setState(() => _refreshing = false);
    }
  }

  @override
  void initState() {
    super.initState();
    Future.microtask(() async {
      await widget.model.loadSettings();
      await widget.model.loadCandidates();
    });
  }

  Future<void> _addDialog() async {
    final name = TextEditingController();
    final url = TextEditingController();
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: const Text('Добавить источник'),
        content: Column(mainAxisSize: MainAxisSize.min, children: [
          TextField(controller: name, decoration: const InputDecoration(labelText: 'Название (латиница, цифры, - _)')),
          const SizedBox(height: 16), // подпись соседнего поля при фокусе заходит на рамку верхнего
          TextField(controller: url, decoration: const InputDecoration(labelText: 'Ссылка (https://…)'), keyboardType: TextInputType.url),
        ]),
        actions: [
          TextButton(onPressed: () => Navigator.pop(c, false), child: const Text('Отмена')),
          FilledButton(onPressed: () => Navigator.pop(c, true), child: const Text('Добавить')),
        ],
      ),
    );
    if (ok == true && name.text.trim().isNotEmpty && url.text.trim().isNotEmpty) {
      await widget.model.addSource(name.text.trim(), url.text.trim());
    }
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) => Scaffold(
        appBar: AppBar(
          title: const Text('Источники подписок'),
          actions: [
            IconButton(tooltip: 'Обновить список источников', icon: const Icon(Icons.cloud_sync_outlined), onPressed: m.settingsLoading ? null : m.updateOtaSources),
            IconButton(tooltip: 'Обновить подписки сейчас', icon: const Icon(Icons.refresh), onPressed: m.settingsLoading || m.nodesLoading ? null : _refresh),
          ],
        ),
        floatingActionButton: FloatingActionButton.extended(onPressed: _addDialog, icon: const Icon(Icons.add), label: const Text('Добавить источник')),
        body: Column(children: [
          SizedBox(height: 2, child: (m.settingsLoading || _refreshing) ? const LinearProgressIndicator(minHeight: 2) : null),
          Expanded(
            child: m.sources.isEmpty && !m.settingsLoading && !m.candidatesOk
                ? const Center(child: Text('Источников нет'))
                : ListView(padding: const EdgeInsets.fromLTRB(16, 8, 16, 88), children: [
                    SwitchListTile(
                      contentPadding: EdgeInsets.zero,
                      secondary: const Icon(Icons.update), // значок слева, как у плитки «Найденные источники» ниже: обе строки выглядят одинаково
                      title: const Text('Список источников обновляется сам'),
                      subtitle: const Text('Раз в сутки, с проверкой подписи; свои источники не меняются'),
                      value: m.otaAuto,
                      onChanged: m.setOtaAuto,
                    ),
                    if (m.candidatesOk)
                      ListTile(
                        key: const Key('candidatesEntry'),
                        contentPadding: EdgeInsets.zero,
                        leading: const Icon(Icons.travel_explore),
                        title: Text('Найденные источники (${bucketByRepo(m.candidateGroups).length} ${ruPlural(bucketByRepo(m.candidateGroups).length, 'репозиторий', 'репозитория', 'репозиториев')})'),
                        subtitle: const Text('Автопоиск по репозиториям; сами не добавляются'),
                        trailing: const Icon(Icons.chevron_right),
                        onTap: () => Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => CandidatesPage(model: m))),
                      ),
                    if (m.sources.isEmpty && !m.settingsLoading) const Padding(padding: EdgeInsets.symmetric(vertical: 16), child: Center(child: Text('Источников нет'))),
                    for (final s in m.sources) SourceTile(model: m, s: s),
                  ]),
          ),
        ]),
      ),
    );
  }
}

class SourceTile extends StatelessWidget {
  const SourceTile({super.key, required this.model, required this.s});

  final AppModel model;
  final SourceDto s;

  static String ago(DateTime? t) {
    if (t == null) return 'ещё не загружался';
    final d = DateTime.now().difference(t.toLocal());
    if (d.inMinutes < 2) return 'только что';
    if (d.inMinutes < 60) return '${d.inMinutes} мин назад';
    if (d.inHours < 24) return '${d.inHours} ч назад';
    return '${d.inDays} дн назад';
  }

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final sub = s.lastError.isNotEmpty ? 'ошибка: ${s.lastError}' : '${s.lastCount} ссылок · ${ago(s.lastOk)}';
    // рейтинг: сколько проверенных за неделю узлов источника ответило; без единого живого из 50+ проверенных источник обновляется раз в сутки
    final yieldLine = s.checked > 0 ? 'живых за неделю: ${s.alive} из ${s.checked} проверенных${s.rarely ? ' · обновляется редко' : ''}' : null;
    final quota = s.totalBytes > 0
        ? Padding(
            padding: const EdgeInsets.only(top: 6),
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              LinearProgressIndicator(value: (s.usedBytes / s.totalBytes).clamp(0, 1).toDouble(), minHeight: 6, borderRadius: BorderRadius.circular(3)),
              const SizedBox(height: 2),
              Text(
                'Осталось ${fmtBytes((s.totalBytes - s.usedBytes).clamp(0, s.totalBytes))} из ${fmtBytes(s.totalBytes)}${s.expire == null ? '' : ' · до ${s.expire!.toLocal().toString().substring(0, 10)}'}',
                style: TextStyle(fontSize: 11, color: cs.onSurfaceVariant),
              ),
            ]),
          )
        : s.expire == null
            ? null
            : Padding(
                padding: const EdgeInsets.only(top: 6),
                child: Text('Подписка действует до ${s.expire!.toLocal().toString().substring(0, 10)}', style: TextStyle(fontSize: 11, color: cs.onSurfaceVariant)),
              );
    return ListTile(
      contentPadding: EdgeInsets.zero,
      title: Text(s.title.isNotEmpty ? '${s.name} · ${s.title}' : s.name, overflow: TextOverflow.ellipsis),
      subtitle: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text('${s.host}\n$sub', style: TextStyle(color: s.lastError.isNotEmpty ? cs.error : null)),
        if (yieldLine != null) Text(yieldLine, style: TextStyle(fontSize: 12, color: s.rarely ? cs.tertiary : cs.onSurfaceVariant)),
        ?quota,
      ]),
      isThreeLine: true,
      trailing: Row(mainAxisSize: MainAxisSize.min, children: [
        if (s.userAdded) IconButton(tooltip: 'Удалить', icon: const Icon(Icons.delete_outline), onPressed: () => model.removeSource(s)),
        Switch(value: s.enabled, onChanged: (v) => model.setSourceEnabled(s, v)),
      ]),
    );
  }
}
