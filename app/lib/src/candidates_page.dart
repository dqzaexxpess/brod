import 'package:flutter/material.dart';

import 'api.dart';
import 'model.dart';

/// Русское склонение по числу: 1 группа, 2 группы, 5 групп, 21 группа.
String ruPlural(int n, String one, String few, String many) {
  final a = n.abs() % 100, b = n.abs() % 10;
  if (a >= 11 && a <= 14) return many;
  if (b == 1) return one;
  if (b >= 2 && b <= 4) return few;
  return many;
}

/// «Найденные источники»: кандидаты автопоиска, сгруппированные по содержимому (зеркала одного набора ссылок вместе). Лежат отдельно от
/// источников и узлов, ничего не добавляется само: «Добавить» нажимает владелец. Один и тот же экран на Android и на компьютере: узкое окно —
/// колонка на всю ширину, широкое — колонка ограниченной ширины по центру.
class CandidatesPage extends StatefulWidget {
  const CandidatesPage({super.key, required this.model});

  final AppModel model;

  @override
  State<CandidatesPage> createState() => _CandidatesPageState();
}

class _CandidatesPageState extends State<CandidatesPage> {
  final _expanded = <String>{}; // группы с раскрытым списком членов
  final _expandedRepos = <String>{}; // репозитории с раскрытым списком наборов

  Widget _groupCard(AppModel m, CandidateGroupDto g) => CandidateGroupCard(
        key: ValueKey(g.id),
        model: m,
        g: g,
        expanded: _expanded.contains(g.id),
        onToggle: () => setState(() => _expanded.contains(g.id) ? _expanded.remove(g.id) : _expanded.add(g.id)),
      );

  @override
  void initState() {
    super.initState();
    Future.microtask(widget.model.loadCandidates);
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    final cs = Theme.of(context).colorScheme;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) => Scaffold(
        appBar: AppBar(
          title: const Text('Найденные источники'),
          actions: [
            TextButton.icon(
              key: const Key('scanNow'),
              onPressed: m.candidatesBusy ? null : m.scanCandidates,
              icon: m.candidatesBusy ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.travel_explore),
              label: Text(m.candidatesBusy ? 'Ищу…' : 'Искать'),
            ),
            const SizedBox(width: 8),
          ],
        ),
        body: Align(
          alignment: Alignment.topCenter,
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 720),
            child: ListView(padding: const EdgeInsets.fromLTRB(16, 8, 16, 24), children: [
              Text(
                'Раз в сутки читается README известных репозиториев. Найденное попадает в источники только после «Добавить».',
                style: TextStyle(fontSize: 12, color: cs.onSurfaceVariant),
              ),
              const SizedBox(height: 8),
              if (m.candidateGroups.isEmpty)
                const Padding(padding: EdgeInsets.symmetric(vertical: 32), child: Center(child: Text('Пока ничего не найдено')))
              else
                for (final b in bucketByRepo(m.candidateGroups))
                  if (b.groups.length == 1)
                    _groupCard(m, b.groups.first)
                  else
                    RepoBucketCard(
                      key: ValueKey('repo-${b.repo}'),
                      bucket: b,
                      expanded: _expandedRepos.contains(b.repo),
                      onToggle: () => setState(() => _expandedRepos.contains(b.repo) ? _expandedRepos.remove(b.repo) : _expandedRepos.add(b.repo)),
                      child: Column(children: [for (final g in b.groups) _groupCard(m, g)]),
                    ),
            ]),
          ),
        ),
      ),
    );
  }
}

/// Репозиторий со всеми найденными в нём наборами ссылок («шарды» m1n1-5ub-1…32 одного репозитория не копии друг друга, но в списке
/// мешают: под одной строкой удобнее).
class RepoBucket {
  RepoBucket(this.repo, this.groups);

  final String repo;
  final List<CandidateGroupDto> groups;

  int get links => groups.fold(0, (a, g) => a + g.representative.links);
  int get sampled => groups.fold(0, (a, g) => a + g.representative.sampled);
  int get sampleOk => groups.fold(0, (a, g) => a + g.representative.sampleOk);
  int get files => groups.fold(0, (a, g) => a + g.size);

  /// Доля живых среди проверенных; null — не проверялось.
  double? get liveShare => sampled > 0 ? sampleOk / sampled : null;
}

/// Группы по репозиторию: сначала проверенные по доле живых, затем по числу ссылок; без репозитория — каждая группа сама по себе.
List<RepoBucket> bucketByRepo(List<CandidateGroupDto> groups) {
  final by = <String, List<CandidateGroupDto>>{};
  for (final g in groups) {
    final k = g.representative.repo.isNotEmpty ? g.representative.repo : '#${g.id}';
    by.putIfAbsent(k, () => []).add(g);
  }
  final out = [for (final e in by.entries) RepoBucket(e.key.startsWith('#') ? e.value.first.representative.name : e.key, e.value)];
  out.sort((a, b) {
    final x = a.liveShare, y = b.liveShare;
    if ((x == null) != (y == null)) return x == null ? 1 : -1;
    if (x != null && y != null && x != y) return y.compareTo(x);
    return b.links.compareTo(a.links);
  });
  return out;
}

/// «vless 90, trojan 10» по убыванию числа ссылок.
String schemesText(Map<String, int> schemes) {
  final l = schemes.entries.toList()..sort((a, b) => b.value != a.value ? b.value.compareTo(a.value) : a.key.compareTo(b.key));
  return l.map((e) => '${e.key} ${e.value}').join(', ');
}

String _live(CandidateDto c) => c.sampled > 0 ? 'живых ${c.sampleOk} из ${c.sampled}' : 'без проверки';

class CandidateGroupCard extends StatelessWidget {
  const CandidateGroupCard({super.key, required this.model, required this.g, required this.expanded, required this.onToggle});

  final AppModel model;
  final CandidateGroupDto g;
  final bool expanded;
  final VoidCallback onToggle;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final r = g.representative;
    final n = g.members.length;
    return Card(
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(r.name, style: const TextStyle(fontWeight: FontWeight.w600), overflow: TextOverflow.ellipsis),
          const SizedBox(height: 2),
          Text('${r.links} ссылок · ${schemesText(r.schemes)}', style: TextStyle(color: cs.onSurfaceVariant)),
          Text('${r.daysSeen} дн. наблюдения, ${_live(r)}', style: TextStyle(fontSize: 12, color: cs.onSurfaceVariant)),
          const SizedBox(height: 4),
          Wrap(alignment: WrapAlignment.end, crossAxisAlignment: WrapCrossAlignment.center, spacing: 8, runSpacing: 4, children: [
            if (n > 0)
              TextButton.icon(
                key: Key('toggle-${g.id}'),
                onPressed: onToggle,
                icon: Icon(expanded ? Icons.expand_less : Icons.expand_more),
                label: Text(expanded ? 'Свернуть' : 'ещё $n ${ruPlural(n, 'похожий', 'похожих', 'похожих')}'),
              ),
            TextButton(
              key: Key('dismiss-${g.id}'),
              onPressed: () => model.dismissCandidateGroup(g),
              child: Text(n > 0 ? 'Скрыть группу' : 'Скрыть'),
            ),
            FilledButton(key: Key('add-${r.id}'), onPressed: () => model.addCandidate(r), child: const Text('Добавить')),
          ]),
          if (expanded) ...[
            const Divider(height: 16),
            for (final c in g.members)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 4),
                child: Row(crossAxisAlignment: CrossAxisAlignment.center, children: [
                  Expanded(
                    child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                      Text(c.name, overflow: TextOverflow.ellipsis),
                      Text('${c.links} ссылок · ${c.daysSeen} дн. наблюдения, ${_live(c)}', style: TextStyle(fontSize: 12, color: cs.onSurfaceVariant)),
                    ]),
                  ),
                  const SizedBox(width: 8),
                  OutlinedButton(key: Key('add-${c.id}'), onPressed: () => model.addCandidate(c), child: const Text('Добавить')),
                ]),
              ),
          ],
        ]),
      ),
    );
  }
}

/// Строка репозитория: сводка по его наборам ссылок, раскрывается в карточки наборов.
class RepoBucketCard extends StatelessWidget {
  const RepoBucketCard({super.key, required this.bucket, required this.expanded, required this.onToggle, required this.child});

  final RepoBucket bucket;
  final bool expanded;
  final VoidCallback onToggle;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final n = bucket.groups.length;
    final live = bucket.sampled > 0 ? 'живых ${bucket.sampleOk} из ${bucket.sampled}' : 'без проверки';
    return Card(
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: Column(children: [
        ListTile(
          key: Key('repo-toggle-${bucket.repo}'),
          onTap: onToggle,
          title: Text(bucket.repo, style: const TextStyle(fontWeight: FontWeight.w600), overflow: TextOverflow.ellipsis),
          subtitle: Text('$n ${ruPlural(n, 'набор', 'набора', 'наборов')} · ${bucket.links} ссылок · $live', style: TextStyle(fontSize: 12, color: cs.onSurfaceVariant)),
          trailing: Icon(expanded ? Icons.expand_less : Icons.expand_more),
        ),
        if (expanded) Padding(padding: const EdgeInsets.fromLTRB(8, 0, 8, 8), child: child),
      ]),
    );
  }
}
