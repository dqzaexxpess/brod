import 'dart:io';

import 'package:flutter/material.dart';
import 'qr_dialog.dart';
import 'package:flutter/services.dart';

import 'android_bridge.dart';
import 'api.dart';
import 'candidates_page.dart' show ruPlural;
import 'country.dart';
import 'extras.dart';
import 'model.dart';
import 'simple_rules.dart';

const _actionNames = {'proxy': 'Через VPN', 'direct': 'Напрямую', 'block': 'Блокировать'};
const _fallbackNames = {'': 'Блокировать', 'proxy': 'Через выход', 'direct': 'Напрямую'};

String _nodeText(NodeDto n) {
  final cc = n.exit?.country ?? '';
  final name = n.label.isNotEmpty ? n.label : '${n.server}:${n.port}';
  return cc.isEmpty ? name : '${flagOf(cc)} $name';
}

String _shortId(String id) => id.length > 8 ? id.substring(0, 8) : id;

/// Выход правила одной строкой для списка («🇳🇱 Нидерланды · сейчас узел …»); пусто, если у правила общий выход.
String viaSummary(RouteRuleDto r, {ViaStateDto? state, List<NodeDto> nodes = const []}) {
  final v = r.via;
  if (r.action != 'proxy' || v == null || v.kind.isEmpty) return '';
  var s = switch (v.kind) {
    'country' => '${flagOf(v.country)} ${countryName(v.country)}',
    'node' => () {
        final n = nodes.where((e) => e.id == v.node);
        return 'узел ${n.isNotEmpty ? _nodeText(n.first) : _shortId(v.node)}';
      }(),
    'warp' => 'WARP',
    _ => v.kind,
  };
  if (state != null) {
    if (state.fallback) {
      s += ' · нет подходящего: ${state.target == 'none' ? 'блокировка' : state.target == 'active' ? 'через выход' : 'напрямую'}';
    } else if (v.kind == 'country' && state.node.isNotEmpty) {
      final n = nodes.where((e) => e.id == state.node);
      s += ' · сейчас ${n.isNotEmpty ? _nodeText(n.first) : _shortId(state.node)}';
    }
  }
  return s;
}

/// Правила маршрутизации (как в Throne): упорядоченный список, первое совпавшее побеждает; работают при включённом VPN.
class RulesPage extends StatefulWidget {
  const RulesPage({super.key, required this.model});

  final AppModel model;

  @override
  State<RulesPage> createState() => _RulesPageState();
}

class _RulesPageState extends State<RulesPage> {
  @override
  void initState() {
    super.initState();
    Future.microtask(widget.model.loadRules);
  }

  Future<void> _edit(int? index) async {
    final m = widget.model;
    final r = await showDialog<RouteRuleDto>(
      context: context,
      builder: (_) => RuleDialog(initial: index == null ? RouteRuleDto() : m.rules[index].copy(), sets: m.ruleSets, setTitles: {for (final u in m.userSets) u.id: u.label}, countries: m.routeCountries, nodes: m.nodes),
    );
    if (r == null) return;
    if (index == null) {
      m.addRule(r);
    } else {
      m.replaceRule(index, r);
    }
  }

  Future<String?> _askText(String title, {String initial = '', String hint = '', int maxLines = 1, List<Widget> extra = const []}) {
    final c = TextEditingController(text: initial);
    return showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(title),
        content: SizedBox(
          width: 420,
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            TextField(controller: c, autofocus: true, minLines: 1, maxLines: maxLines, decoration: InputDecoration(hintText: hint, border: const OutlineInputBorder(), isDense: true)),
            ...extra,
          ]),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Отмена')),
          FilledButton(onPressed: () => Navigator.pop(ctx, c.text), child: const Text('Готово')),
        ],
      ),
    );
  }

  /// Операции с профилем перезагружают правила с сервера: несохранённые правки пропали бы.
  Future<bool> _discardOk() async {
    if (!widget.model.rulesDirty) return true;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Несохранённые изменения'),
        content: const Text('Неприменённые правки пропадут.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Продолжить')),
        ],
      ),
    );
    return ok == true;
  }

  Future<void> _profiles() async {
    final m = widget.model;
    await showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      isScrollControlled: true,
      builder: (ctx) => SafeArea(
        child: ListenableBuilder(
          listenable: m,
          builder: (context, _) => ConstrainedBox(
            constraints: BoxConstraints(maxHeight: MediaQuery.of(context).size.height * 0.8),
            child: ListView(shrinkWrap: true, children: [
              const Padding(padding: EdgeInsets.fromLTRB(20, 0, 20, 8), child: Text('У каждого профиля свой набор правил; переключение перезапускает VPN')),
              for (final p in m.profiles)
                ListTile(
                  leading: Icon(p.active ? Icons.radio_button_checked : Icons.radio_button_unchecked),
                  title: Text(p.name),
                  subtitle: Text('${p.rules} ${ruPlural(p.rules, 'правило', 'правила', 'правил')}${p.active ? ' · активный' : ''}'),
                  onTap: p.active
                      ? null
                      : () async {
                          if (!await _discardOk()) return;
                          if (ctx.mounted) Navigator.pop(ctx);
                          await m.switchProfile(p.id);
                        },
                  trailing: PopupMenuButton<String>(
                    onSelected: (v) async {
                      switch (v) {
                        case 'rename':
                          final n = await _askText('Название профиля', initial: p.name);
                          if (n != null && n.trim().isNotEmpty) await m.renameProfile(p.id, n.trim());
                        case 'share':
                          final link = await m.shareProfile(p.id);
                          if (link != null) {
                            await Clipboard.setData(ClipboardData(text: link));
                            if (mounted) ScaffoldMessenger.of(this.context).showSnackBar(const SnackBar(content: Text('Ссылка на профиль скопирована')));
                          }
                        case 'qr':
                          final link = await m.shareProfile(p.id);
                          if (link != null && mounted) await showQrDialog(this.context, title: 'QR-код профиля «${p.name}»', link: link);
                        case 'delete':
                          if (await _discardOk()) await m.deleteProfile(p.id);
                      }
                    },
                    itemBuilder: (_) => [
                      const PopupMenuItem(value: 'rename', child: Text('Переименовать')),
                      const PopupMenuItem(value: 'share', child: Text('Скопировать ссылку')),
                      const PopupMenuItem(value: 'qr', child: Text('Показать QR-код')),
                      if (!p.active) const PopupMenuItem(value: 'delete', child: Text('Удалить')),
                    ],
                  ),
                ),
              const Divider(),
              ListTile(
                leading: const Icon(Icons.add),
                title: const Text('Новый пустой профиль…'),
                onTap: () async {
                  final n = await _askText('Название нового профиля');
                  if (n != null && n.trim().isNotEmpty && await _discardOk()) await m.createProfile(n.trim());
                },
              ),
              ListTile(
                leading: const Icon(Icons.copy_all_outlined),
                title: const Text('Копия активного профиля…'),
                onTap: () async {
                  final a = m.activeProfile;
                  if (a == null) return;
                  final n = await _askText('Название копии', initial: 'Копия «${a.name}»');
                  if (n != null && n.trim().isNotEmpty && await _discardOk()) await m.createProfile(n.trim(), from: a.id);
                },
              ),
              ListTile(
                leading: const Icon(Icons.link),
                title: const Text('Добавить профиль из ссылки…'),
                subtitle: const Text('brod://route/… или https-адрес; скачивается один раз, сам не включается'),
                onTap: () async {
                  // в буфере уже может лежать ссылка: подставляем её
                  final clip = (await Clipboard.getData('text/plain'))?.text?.trim() ?? '';
                  final t = await _askText('Ссылка на профиль', initial: (clip.startsWith('brod://route/') || clip.startsWith('https://')) ? clip : '', hint: 'brod://route/… или https://…', maxLines: 3);
                  if (t != null && t.trim().isNotEmpty && await _discardOk()) await m.importProfile(t.trim());
                },
              ),
            ]),
          ),
        ),
      ),
    );
  }

  /// Простые правила построчно (как в Throne): «example.com», «domain:…», «keyword:…», «ip:…», «process:…», «port:…», «geosite:…».
  Future<void> _addList() async {
    final m = widget.model;
    var action = 'proxy';
    final c = TextEditingController();
    String? err;
    final done = await showDialog<bool>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setS) => AlertDialog(
          title: const Text('Добавить правила списком'),
          content: SizedBox(
            width: 460,
            child: SingleChildScrollView(
              child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
                SegmentedButton<String>(
                  showSelectedIcon: false,
                  segments: [for (final e in _actionNames.entries) ButtonSegment(value: e.key, label: Text(e.value))],
                  selected: {action},
                  onSelectionChanged: (s) => setS(() => action = s.first),
                ),
                const SizedBox(height: 10),
                TextField(
                  controller: c,
                  minLines: 6,
                  maxLines: 12,
                  decoration: const InputDecoration(
                    hintText: 'example.com\ndomain:www.exact.example\nkeyword:tracker\nregex:^ads\\d+\\.example\\.com\$\n203.0.113.0/24\nprocess:firefox\nport:8000-9000\nprotocol:tls\ngeosite:youtube',
                    border: OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
                const SizedBox(height: 6),
                Text('Одна строка — одно условие. Разные виды условий станут отдельными правилами.',
                    style: Theme.of(ctx).textTheme.bodySmall),
                if (err != null) Padding(padding: const EdgeInsets.only(top: 8), child: Text(err!, style: TextStyle(color: Theme.of(ctx).colorScheme.error))),
              ]),
            ),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
            FilledButton(
              onPressed: () {
                final r = parseSimpleLines(c.text, knownSets: m.ruleSets);
                if (r.groups.isEmpty) {
                  setS(() => err = r.errors.isEmpty ? 'Список пуст' : r.errors.take(6).join('\n'));
                  return;
                }
                for (final e in r.groups.entries) {
                  m.addRule(RouteRuleDto(title: 'Список: ${e.key}', action: action, match: e.value));
                }
                if (r.errors.isNotEmpty) m.toast = 'Добавлено правил: ${r.groups.length}. Пропущено строк: ${r.errors.length} (${r.errors.first})';
                Navigator.pop(ctx, true);
              },
              child: const Text('Добавить'),
            ),
          ],
        ),
      ),
    );
    if (done == true && mounted) setState(() {});
  }

  Future<void> _presets(BuildContext context) async {
    final m = widget.model;
    await showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      isScrollControlled: true, // шаблонов больше, чем помещается в шторку: без этого нижние обрезались
      builder: (ctx) => SafeArea(
        child: SingleChildScrollView(child: Column(mainAxisSize: MainAxisSize.min, children: [
          const Padding(padding: EdgeInsets.fromLTRB(20, 0, 20, 8), child: Align(alignment: Alignment.centerLeft, child: Text('Шаблоны добавляются в список; затем нажмите «Применить»'))),
          for (final p in rulePresets.where((p) => !(p.desktopOnly && Platform.isAndroid)))
            ListTile(
              leading: Icon(p.icon),
              title: Text(p.title),
              subtitle: Text(p.subtitle),
              trailing: m.rules.any((r) => r.title.startsWith(p.title)) ? const Icon(Icons.check, color: Colors.green) : const Icon(Icons.add),
              onTap: m.rules.any((r) => r.title.startsWith(p.title))
                  ? null
                  : () {
                      p.rules().forEach(m.addRule);
                      Navigator.pop(ctx);
                    },
            ),
        ])),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final cs = Theme.of(context).colorScheme;
        return Scaffold(
          appBar: AppBar(
            title: m.profiles.isEmpty
                ? const Text('Правила маршрутизации')
                : InkWell(
                    onTap: _profiles,
                    borderRadius: BorderRadius.circular(8),
                    child: Padding(
                      padding: const EdgeInsets.symmetric(vertical: 4),
                      child: Row(mainAxisSize: MainAxisSize.min, children: [
                        Flexible(child: Text(m.activeProfile?.name ?? 'Правила маршрутизации', overflow: TextOverflow.ellipsis)),
                        const Icon(Icons.arrow_drop_down),
                      ]),
                    ),
                  ),
            actions: [
              IconButton(tooltip: 'Добавить списком', icon: const Icon(Icons.playlist_add), onPressed: _addList),
              IconButton(tooltip: 'Шаблоны правил', icon: const Icon(Icons.auto_awesome_mosaic_outlined), onPressed: () => _presets(context)),
              if (m.rulesDirty)
                Padding(
                  padding: const EdgeInsets.only(right: 8),
                  child: FilledButton(onPressed: m.rulesLoading ? null : () async {
                    await m.saveRules();
                  }, child: const Text('Применить')),
                ),
            ],
          ),
          floatingActionButton: FloatingActionButton.extended(onPressed: () => _edit(null), icon: const Icon(Icons.add), label: const Text('Добавить правило')),
          body: Column(children: [
            SizedBox(height: 2, child: (m.rulesLoading) ? const LinearProgressIndicator(minHeight: 2) : null),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
              child: Text(
                'Срабатывает первое подходящее правило сверху, раньше списков и режима. «Применить» перезапускает VPN.',
                style: Theme.of(context).textTheme.bodySmall?.copyWith(color: cs.onSurfaceVariant),
              ),
            ),
            Expanded(
              child: m.rules.isEmpty && !m.rulesLoading
                  ? const Center(child: Text('Правил нет'))
                  : ReorderableListView.builder(
                      padding: const EdgeInsets.only(bottom: 88),
                      buildDefaultDragHandles: true,
                      itemCount: m.rules.length,
                      onReorderItem: (o, n) => m.moveRule(o, n > o ? n + 1 : n),
                      itemBuilder: (context, i) {
                        final r = m.rules[i];
                        final color = switch (r.action) { 'direct' => Colors.green, 'block' => cs.error, _ => cs.primary };
                        return ListTile(
                          key: ValueKey('${r.id}#$i'),
                          leading: CircleAvatar(radius: 16, child: Text('${i + 1}', style: const TextStyle(fontSize: 12))),
                          title: Text(r.title.isNotEmpty ? r.title : (r.match.summary.isEmpty ? 'Без условий' : r.match.summary), overflow: TextOverflow.ellipsis),
                          subtitle: Row(children: [
                            Text(_actionNames[r.action] ?? r.action, style: TextStyle(color: color, fontWeight: FontWeight.w600)),
                            if (viaSummary(r).isNotEmpty) Flexible(child: Text(' → ${viaSummary(r, state: m.viaStates[r.id], nodes: m.nodes)}', overflow: TextOverflow.ellipsis)),
                            if (r.title.isNotEmpty) Expanded(child: Text(' · ${r.match.summary}', overflow: TextOverflow.ellipsis)),
                          ]),
                          onTap: () => _edit(i),
                          trailing: Row(mainAxisSize: MainAxisSize.min, children: [
                            Switch(value: r.enabled, onChanged: (v) => m.replaceRule(i, r.copy()..enabled = v)),
                            IconButton(tooltip: 'Удалить', icon: const Icon(Icons.delete_outline), onPressed: () => m.removeRule(i)),
                            const SizedBox(width: 28), // место под ручку перетаскивания
                          ]),
                        );
                      },
                    ),
            ),
          ]),
        );
      },
    );
  }
}

/// Диалог правила: название, действие и условия.
class RuleDialog extends StatefulWidget {
  const RuleDialog({super.key, required this.initial, required this.sets, this.setTitles = const {}, this.countries = const [], this.nodes = const []});

  final RouteRuleDto initial;
  final List<String> sets;
  final Map<String, String> setTitles; // названия своих списков по id
  final List<CountryDto> countries; // страны с проверенными узлами
  final List<NodeDto> nodes; // узлы для выбора конкретного

  @override
  State<RuleDialog> createState() => _RuleDialogState();
}

class _RuleDialogState extends State<RuleDialog> {
  late final RouteRuleDto r = widget.initial;
  late final title = TextEditingController(text: r.title);
  late final suffix = TextEditingController(text: r.match.domainSuffix.join('\n'));
  late final domain = TextEditingController(text: r.match.domain.join('\n'));
  late final keyword = TextEditingController(text: r.match.domainKeyword.join('\n'));
  late final regex = TextEditingController(text: r.match.domainRegex.join('\n'));
  late final cidr = TextEditingController(text: r.match.ipCidr.join('\n'));
  late final ports = TextEditingController(text: [...r.match.port.map((e) => '$e'), ...r.match.portRange].join(', '));
  late final procs = TextEditingController(text: r.match.processName.join('\n'));
  late final srcCidr = TextEditingController(text: r.match.sourceIpCidr.join('\n'));
  late final srcPorts = TextEditingController(text: [...r.match.sourcePort.map((e) => '$e'), ...r.match.sourcePortRange].join(', '));
  late final procPath = TextEditingController(text: r.match.processPath.join('\n'));
  late final procPathRe = TextEditingController(text: r.match.processPathRegex.join('\n'));
  late final packages = TextEditingController(text: r.match.packageName.join('\n'));
  late final wifiSsid = TextEditingController(text: r.match.wifiSsid.join('\n'));
  late final wifiBssid = TextEditingController(text: r.match.wifiBssid.join('\n'));
  late final Set<String> protocols = {...r.match.protocol};
  late int ipVersion = r.match.ipVersion;
  late bool ipIsPrivate = r.match.ipIsPrivate;
  late bool srcIsPrivate = r.match.sourceIpIsPrivate;
  late bool invert = r.match.invert;
  late String blockMethod = r.blockMethod;
  late bool tlsFragment = r.tlsFragment;
  late bool tlsRecordFragment = r.tlsRecordFragment;
  late final TextEditingController tlsSpoof = TextEditingController(text: r.tlsSpoof);
  late final Set<String> sets = {...r.match.ruleSet};
  late final RouteViaDto via = r.via == null ? RouteViaDto() : RouteViaDto(kind: r.via!.kind, node: r.via!.node, country: r.via!.country, fallback: r.via!.fallback);
  String? error;

  static List<String> _lines(String t) => t.split(RegExp(r'[\n,;]')).map((e) => e.trim()).where((e) => e.isNotEmpty).toList();

  Widget _field(TextEditingController c, String label, {String? hint}) => Padding(
        padding: const EdgeInsets.only(top: 10),
        child: TextField(controller: c, minLines: 1, maxLines: 4, decoration: InputDecoration(labelText: label, hintText: hint, border: const OutlineInputBorder(), isDense: true)),
      );

  /// Выбор выхода правила: авто (общий диспетчер), страна, конкретный узел или WARP.
  List<Widget> _viaSection() {
    final countries = [...widget.countries];
    if (via.kind == 'country' && via.country.isNotEmpty && !countries.any((c) => c.code == via.country)) countries.add(CountryDto(via.country, 0, 0));
    final selectedNode = widget.nodes.where((n) => n.id == via.node);
    return [
      const SizedBox(height: 12),
      Text('Выход', style: Theme.of(context).textTheme.labelMedium),
      const SizedBox(height: 6),
      SegmentedButton<String>(
        showSelectedIcon: false,
        segments: const [
          ButtonSegment(value: '', label: Text('Авто')),
          ButtonSegment(value: 'country', label: Text('Страна')),
          ButtonSegment(value: 'node', label: Text('Узел')),
          ButtonSegment(value: 'warp', label: Text('WARP')),
        ],
        selected: {via.kind},
        onSelectionChanged: (s) => setState(() => via.kind = s.first),
      ),
      if (via.kind == '')
        Padding(
          padding: const EdgeInsets.only(top: 6),
          child: Text('Как у остальных: активный узел и цели.', style: Theme.of(context).textTheme.bodySmall),
        ),
      if (via.kind == 'country') ...[
        const SizedBox(height: 8),
        DropdownButtonFormField<String>(
          initialValue: via.country.isEmpty ? null : via.country,
          isExpanded: true,
          decoration: const InputDecoration(labelText: 'Страна выхода', border: OutlineInputBorder(), isDense: true),
          items: [
            for (final c in countries)
              DropdownMenuItem(value: c.code, child: Text('${flagOf(c.code)} ${countryName(c.code)} · живых ${c.alive} из ${c.total}', overflow: TextOverflow.ellipsis)),
          ],
          onChanged: (v) => setState(() => via.country = v ?? ''),
        ),
        Padding(
          padding: const EdgeInsets.only(top: 6),
          child: Text('Берётся лучший живой узел этой страны; при отказе заменяется сам.', style: Theme.of(context).textTheme.bodySmall),
        ),
      ],
      if (via.kind == 'node') ...[
        const SizedBox(height: 8),
        OutlinedButton.icon(
          icon: const Icon(Icons.dns_outlined),
          label: Text(via.node.isEmpty ? 'Выбрать узел…' : (selectedNode.isNotEmpty ? _nodeText(selectedNode.first) : 'Узел ${_shortId(via.node)}…'), overflow: TextOverflow.ellipsis),
          onPressed: () async {
            final id = await showDialog<String>(context: context, builder: (_) => _NodePicker(nodes: widget.nodes, current: via.node));
            if (id != null) setState(() => via.node = id);
          },
        ),
      ],
      if (via.kind.isNotEmpty) ...[
        const SizedBox(height: 10),
        DropdownButtonFormField<String>(
          initialValue: via.fallback,
          isExpanded: true,
          decoration: const InputDecoration(labelText: 'Если подходящего выхода нет', border: OutlineInputBorder(), isDense: true),
          items: [for (final e in _fallbackNames.entries) DropdownMenuItem(value: e.key, child: Text(e.value))],
          onChanged: (v) => setState(() => via.fallback = v ?? ''),
        ),
      ],
    ];
  }

  void _save() {
    final p = <int>[];
    final pr = <String>[];
    for (final e in _lines(ports.text)) {
      final n = int.tryParse(e);
      if (n != null) {
        p.add(n);
      } else {
        pr.add(e);
      }
    }
    final sp = <int>[];
    final spr = <String>[];
    for (final e in _lines(srcPorts.text)) {
      final n = int.tryParse(e);
      if (n != null) {
        sp.add(n);
      } else {
        spr.add(e);
      }
    }
    final m = RouteMatch(
      ipVersion: ipVersion,
      protocol: protocols.toList(),
      sourceIpCidr: _lines(srcCidr.text),
      ipIsPrivate: ipIsPrivate,
      sourceIpIsPrivate: srcIsPrivate,
      sourcePort: sp,
      sourcePortRange: spr,
      processPath: _lines(procPath.text),
      processPathRegex: _lines(procPathRe.text),
      packageName: _lines(packages.text),
      wifiSsid: _lines(wifiSsid.text),
      wifiBssid: _lines(wifiBssid.text),
      extra: Map.of(r.match.extra),
      invert: invert,
      domainSuffix: _lines(suffix.text),
      domain: _lines(domain.text),
      domainKeyword: _lines(keyword.text),
      domainRegex: _lines(regex.text),
      ipCidr: _lines(cidr.text),
      port: p,
      portRange: pr,
      network: r.match.network,
      processName: _lines(procs.text),
      ruleSet: sets.toList(),
    );
    if (m.isEmpty) {
      setState(() => error = 'Задайте хотя бы одно условие');
      return;
    }
    RouteViaDto? v;
    if (r.action == 'proxy' && via.kind.isNotEmpty) {
      if (via.kind == 'country' && via.country.isEmpty) {
        setState(() => error = 'Выберите страну');
        return;
      }
      if (via.kind == 'node' && via.node.isEmpty) {
        setState(() => error = 'Выберите узел');
        return;
      }
      v = via;
    }
    Navigator.pop(context, RouteRuleDto(id: r.id, title: title.text.trim(), enabled: r.enabled, action: r.action, match: m, via: v, blockMethod: r.action == 'block' ? blockMethod : '', tlsFragment: r.action != 'block' && tlsFragment, tlsRecordFragment: r.action != 'block' && tlsRecordFragment, tlsSpoof: r.action != 'block' ? tlsSpoof.text.trim() : '', tlsSpoofMethod: r.action != 'block' && tlsSpoof.text.trim().isNotEmpty ? r.tlsSpoofMethod : ''));
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text(r.id.isEmpty ? 'Новое правило' : 'Правило'),
      content: SizedBox(
        width: 460,
        child: SingleChildScrollView(
          child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
            TextField(controller: title, decoration: const InputDecoration(labelText: 'Название (необязательно)', border: OutlineInputBorder(), isDense: true)),
            const SizedBox(height: 12),
            SegmentedButton<String>(
              showSelectedIcon: false,
              segments: [for (final e in _actionNames.entries) ButtonSegment(value: e.key, label: Text(e.value))],
              selected: {r.action},
              onSelectionChanged: (s) => setState(() => r.action = s.first),
            ),
            if (r.action == 'proxy') ..._viaSection(),
            if (r.action != 'block') ...[
              const SizedBox(height: 6),
              ExpansionTile(
                tilePadding: EdgeInsets.zero,
                title: const Text('Обход DPI (дробление TLS)'),
                initiallyExpanded: tlsFragment || tlsRecordFragment || tlsSpoof.text.isNotEmpty,
                children: [
                  CheckboxListTile(
                    contentPadding: EdgeInsets.zero,
                    title: const Text('Дробить TLS ClientHello'),
                    subtitle: const Text('Если провайдер режет по SNI. Помогает не везде; лучше «напрямую»'),
                    value: tlsFragment,
                    onChanged: (v) => setState(() => tlsFragment = v ?? false),
                  ),
                  CheckboxListTile(
                    contentPadding: EdgeInsets.zero,
                    title: const Text('Дробить записи TLS'),
                    subtitle: const Text('Делит запись, а не пакет. Вместе с первым или вместо него'),
                    value: tlsRecordFragment,
                    onChanged: (v) => setState(() => tlsRecordFragment = v ?? false),
                  ),
                  if (!Platform.isAndroid)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 8),
                      child: TextField(
                        controller: tlsSpoof,
                        decoration: const InputDecoration(
                          labelText: 'Подмена SNI (имя сайта)',
                          hintText: 'www.cloudflare.com',
                          helperText: 'Ложный пакет с этим именем уходит перед настоящим запросом. Только Linux (нужен cap_net_raw) и Windows',
                          helperMaxLines: 2,
                        ),
                      ),
                    ),
                ],
              ),
            ],
            if (r.action == 'block') ...[
              const SizedBox(height: 10),
              Row(children: [
                const Text('Блокировка: '),
                const SizedBox(width: 8),
                SegmentedButton<String>(
                  showSelectedIcon: false,
                  segments: const [ButtonSegment(value: '', label: Text('отказ')), ButtonSegment(value: 'drop', label: Text('молча'))],
                  selected: {blockMethod},
                  onSelectionChanged: (s) => setState(() => blockMethod = s.first),
                ),
              ]),
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text('Разница только для UDP: «молча» не шлёт ответ ICMP.', style: Theme.of(context).textTheme.bodySmall),
              ),
            ],
            const SizedBox(height: 14),
            Text('Условия: по строке или через запятую, разные поля — «И»', style: Theme.of(context).textTheme.labelMedium),
            _field(suffix, 'Сайты (домен и поддомены)', hint: 'example.com'),
            _field(domain, 'Точные домены', hint: 'www.example.com'),
            _field(keyword, 'Слово в домене', hint: 'tracker'),
            _field(regex, 'Регулярное выражение домена'),
            _field(cidr, 'IP-адреса и подсети', hint: '203.0.113.0/24'),
            _field(ports, 'Порты и диапазоны', hint: '443, 8000:9000'),
            _field(procs, 'Процессы', hint: 'firefox'),
            const SizedBox(height: 10),
            Row(children: [
              const Text('Сеть: '),
              const SizedBox(width: 8),
              SegmentedButton<String>(
                showSelectedIcon: false,
                segments: const [ButtonSegment(value: '', label: Text('любая')), ButtonSegment(value: 'tcp', label: Text('TCP')), ButtonSegment(value: 'udp', label: Text('UDP'))],
                selected: {r.match.network},
                onSelectionChanged: (s) => setState(() => r.match.network = s.first),
              ),
            ]),
            const SizedBox(height: 6),
            ExpansionTile(
              tilePadding: EdgeInsets.zero,
              initiallyExpanded: ipVersion != 0 || protocols.isNotEmpty || srcCidr.text.isNotEmpty || srcPorts.text.isNotEmpty || procPath.text.isNotEmpty || procPathRe.text.isNotEmpty || packages.text.isNotEmpty || wifiSsid.text.isNotEmpty || wifiBssid.text.isNotEmpty || ipIsPrivate || srcIsPrivate || invert,
              title: Text('Дополнительные условия', style: Theme.of(context).textTheme.labelMedium),
              children: [
                Align(
                  alignment: Alignment.centerLeft,
                  child: SegmentedButton<int>(
                    showSelectedIcon: false,
                    segments: const [ButtonSegment(value: 0, label: Text('любой IP')), ButtonSegment(value: 4, label: Text('IPv4')), ButtonSegment(value: 6, label: Text('IPv6'))],
                    selected: {ipVersion},
                    onSelectionChanged: (s) => setState(() => ipVersion = s.first),
                  ),
                ),
                const SizedBox(height: 8),
                Align(alignment: Alignment.centerLeft, child: Text('Протокол (определяется по началу соединения)', style: Theme.of(context).textTheme.bodySmall)),
                Wrap(spacing: 6, runSpacing: 0, children: [
                  for (final p in const ['http', 'tls', 'quic', 'dns', 'bittorrent', 'stun', 'dtls', 'ssh', 'rdp', 'ntp'])
                    FilterChip(label: Text(p, style: const TextStyle(fontSize: 12)), selected: protocols.contains(p), onSelected: (v) => setState(() => v ? protocols.add(p) : protocols.remove(p))),
                ]),
                _field(srcCidr, 'IP-адреса и подсети отправителя', hint: '192.168.1.0/24'),
                _field(srcPorts, 'Порты отправителя', hint: '5000, 6000:6100'),
                _field(procPath, 'Пути процессов (Linux, Windows)', hint: '/usr/bin/curl'),
                _field(procPathRe, 'Регулярное выражение пути процесса', hint: r'^/opt/.*/tool$'),
                _field(packages, 'Пакеты приложений (Android)', hint: 'org.telegram.messenger'),
                _field(wifiSsid, 'Сеть Wi-Fi (имя)', hint: 'HomeWiFi'),
                _field(wifiBssid, 'Точка доступа Wi-Fi (адрес)', hint: 'aa:bb:cc:dd:ee:ff'),
                if (Platform.isAndroid) const _LocationNote(),
                SwitchListTile(contentPadding: EdgeInsets.zero, dense: true, title: const Text('Назначение — локальный адрес'), value: ipIsPrivate, onChanged: (v) => setState(() => ipIsPrivate = v)),
                SwitchListTile(contentPadding: EdgeInsets.zero, dense: true, title: const Text('Отправитель — локальный адрес'), value: srcIsPrivate, onChanged: (v) => setState(() => srcIsPrivate = v)),
                SwitchListTile(
                    contentPadding: EdgeInsets.zero,
                    dense: true,
                    title: const Text('Инвертировать'),
                    subtitle: const Text('Применить правило, если условия НЕ совпали'),
                    value: invert,
                    onChanged: (v) => setState(() => invert = v)),
              ],
            ),
            if (widget.sets.isNotEmpty) ...[
              const SizedBox(height: 12),
              Text(widget.setTitles.isEmpty ? 'Встроенные списки' : 'Списки (встроенные и свои)', style: Theme.of(context).textTheme.labelMedium),
              Wrap(spacing: 6, runSpacing: 0, children: [
                for (final s in widget.sets)
                  FilterChip(label: Text(widget.setTitles[s] ?? s.replaceFirst('geosite-', '').replaceFirst('geoip-', 'IP: '), style: const TextStyle(fontSize: 12)), selected: sets.contains(s), onSelected: (v) => setState(() => v ? sets.add(s) : sets.remove(s))),
              ]),
            ],
            if (error != null) Padding(padding: const EdgeInsets.only(top: 10), child: Text(error!, style: TextStyle(color: Theme.of(context).colorScheme.error))),
          ]),
        ),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(context), child: const Text('Отмена')),
        FilledButton(onPressed: _save, child: const Text('Готово')),
      ],
    );
  }
}

/// Выбор узла для правила: список загруженных узлов с поиском (живые и избранные первыми, как на вкладке «Узлы»).
class _NodePicker extends StatefulWidget {
  const _NodePicker({required this.nodes, required this.current});

  final List<NodeDto> nodes;
  final String current;

  @override
  State<_NodePicker> createState() => _NodePickerState();
}

class _NodePickerState extends State<_NodePicker> {
  String q = '';

  @override
  Widget build(BuildContext context) {
    final low = q.trim().toLowerCase();
    final list = widget.nodes.where((n) => low.isEmpty || _nodeText(n).toLowerCase().contains(low) || n.proto.toLowerCase().contains(low) || (n.exit != null && countryName(n.exit!.country).toLowerCase().contains(low))).toList();
    return AlertDialog(
      title: const Text('Выбор узла'),
      content: SizedBox(
        width: 460,
        height: 420,
        child: Column(children: [
          TextField(onChanged: (v) => setState(() => q = v), decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: 'Поиск по названию, адресу, стране', border: OutlineInputBorder(), isDense: true)),
          const SizedBox(height: 8),
          Expanded(
            child: list.isEmpty
                ? const Center(child: Text('Нет узлов (откройте вкладку «Узлы» и обновите список)'))
                : ListView.builder(
                    itemCount: list.length,
                    itemBuilder: (_, i) {
                      final n = list[i];
                      return ListTile(
                        dense: true,
                        selected: n.id == widget.current,
                        leading: Icon(n.l2At != null ? Icons.circle : Icons.circle_outlined, size: 12, color: n.l2At != null ? Colors.green : null),
                        title: Text(_nodeText(n), overflow: TextOverflow.ellipsis),
                        subtitle: Text('${n.proto}${n.latencyMs > 0 ? ' · ${n.latencyMs} мс' : ''}${n.favorite ? ' · ★' : ''}'),
                        onTap: () => Navigator.pop(context, n.id),
                      );
                    },
                  ),
          ),
        ]),
      ),
      actions: [TextButton(onPressed: () => Navigator.pop(context), child: const Text('Отмена'))],
    );
  }
}

/// Android отдаёт имя сети Wi-Fi только приложению с разрешением на точное местоположение и при включённой геолокации.
class _LocationNote extends StatefulWidget {
  const _LocationNote();

  @override
  State<_LocationNote> createState() => _LocationNoteState();
}

class _LocationNoteState extends State<_LocationNote> {
  bool? granted;

  @override
  void initState() {
    super.initState();
    AndroidCore.locationGranted().then((v) {
      if (mounted) setState(() => granted = v);
    });
  }

  @override
  Widget build(BuildContext context) {
    final small = Theme.of(context).textTheme.bodySmall;
    if (granted == null) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: granted!
          ? Text('Разрешение есть. Нужна включённая геолокация; после выдачи разрешения перезапустите VPN.', style: small)
          : Row(crossAxisAlignment: CrossAxisAlignment.center, children: [
              Expanded(child: Text('Чтобы Android отдал имя сети Wi-Fi, нужны разрешение на местоположение «Разрешить всегда» и включённая геолокация. Само местоположение Brod не использует.', style: small)),
              TextButton(
                onPressed: () async {
                  final ok = await AndroidCore.requestLocation();
                  if (mounted) setState(() => granted = ok);
                },
                child: const Text('Разрешить'),
              ),
            ]),
    );
  }
}
