import 'package:flutter/material.dart';

import 'android_bridge.dart';
import 'model.dart';
import 'widgets.dart';

/// Android: для каких приложений работает VPN — для всех, только для выбранных или для всех, кроме выбранных (per-app).
class AppsPage extends StatefulWidget {
  const AppsPage({super.key, required this.model});

  final AppModel model;

  @override
  State<AppsPage> createState() => _AppsPageState();
}

class _AppsPageState extends State<AppsPage> {
  late String _mode;
  late Set<String> _selected;
  late String _modeWas;
  late Set<String> _selectedWas;
  bool _touched = false; // пользователь уже что-то менял: пришедшие позже настройки его выбор не затирают
  List<AppInfo>? _apps;
  String? _error;
  String _q = '';
  bool _saving = false;

  @override
  void initState() {
    super.initState();
    _syncFromSettings();
    // настройки загружаются, только когда открыта вкладка «Настройки», и в фоне: если страницу открыли раньше, чем они пришли
    // (или ядро было занято перезапуском), карта пуста и все галочки были бы сняты. Поэтому страница сама просит настройки и
    // подхватывает их, пока пользователь ничего не менял.
    widget.model.addListener(_onModel);
    Future.microtask(widget.model.loadSettings);
    AndroidCore.listApps().then((l) {
      if (mounted) setState(() => _apps = l);
    }).catchError((Object e) {
      if (mounted) setState(() => _error = '$e');
    });
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModel);
    super.dispose();
  }

  void _syncFromSettings() {
    final s = widget.model.settings;
    _mode = (s['tun.apps_mode'] ?? '').isEmpty ? 'off' : s['tun.apps_mode']!;
    _selected = {for (final p in (s['tun.apps'] ?? '').split(',')) if (p.trim().isNotEmpty) p.trim()};
    _modeWas = _mode;
    _selectedWas = {..._selected};
  }

  void _onModel() {
    if (!mounted || _touched || _saving) return;
    final before = '$_mode|${(_selected.toList()..sort()).join(',')}';
    _syncFromSettings();
    if (before != '$_mode|${(_selected.toList()..sort()).join(',')}') setState(() {});
  }

  bool get _changed => _mode != _modeWas || _selected.length != _selectedWas.length || !_selected.containsAll(_selectedWas);

  Future<void> _apply() async {
    if (_mode == 'include' && _selected.isEmpty) {
      showToast(context, 'Выберите хотя бы одно приложение или режим «Все приложения»', error: true);
      return;
    }
    setState(() => _saving = true);
    // одна запись: режим и список вместе, VPN перезапускается один раз; страница закрывается только при успехе, ошибка остаётся на экране
    final err = await widget.model.setTunApps(_mode, _selected.toList()..sort());
    if (!mounted) return;
    setState(() => _saving = false);
    if (err == null) Navigator.of(context).pop();
  }

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final q = _q.trim().toLowerCase();
    final all = _apps ?? const <AppInfo>[];
    final shown = q.isEmpty ? all : all.where((a) => a.label.toLowerCase().contains(q) || a.package.toLowerCase().contains(q)).toList();
    return Scaffold(
      appBar: AppBar(
        title: const Text('Приложения'),
        actions: [TextButton(onPressed: _changed && !_saving ? _apply : null, child: const Text('Применить'))],
      ),
      body: Column(children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
          child: Wrap(spacing: 8, runSpacing: 8, children: [
            for (final e in const {'off': 'Все приложения', 'include': 'Только выбранные', 'exclude': 'Все, кроме выбранных'}.entries)
              ChoiceChip(label: Text(e.value), selected: _mode == e.key, showCheckmark: false, onSelected: (_) => setState(() {
                _touched = true;
                _mode = e.key;
              })),
          ]),
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
          child: Text(
            _mode == 'off'
                ? 'VPN для всех приложений'
                : (_mode == 'include' ? 'Через VPN только выбранные приложения' : 'Выбранные идут напрямую, остальные через VPN'),
            style: TextStyle(fontSize: 12, color: cs.onSurfaceVariant),
          ),
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 4),
          child: TextField(
            decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: 'Найти приложение', isDense: true, border: OutlineInputBorder()),
            onChanged: (v) => setState(() => _q = v),
          ),
        ),
        Expanded(
          child: _error != null
              ? Center(child: Padding(padding: const EdgeInsets.all(24), child: Text('Не удалось получить список приложений: $_error', textAlign: TextAlign.center)))
              : _apps == null
                  ? const Center(child: CircularProgressIndicator())
                  : ListView.builder(
                      itemCount: shown.length,
                      itemBuilder: (context, i) {
                        final a = shown[i];
                        return CheckboxListTile(
                          key: ValueKey(a.package),
                          dense: true,
                          title: Text(a.label, overflow: TextOverflow.ellipsis),
                          subtitle: Text(a.package, overflow: TextOverflow.ellipsis, style: const TextStyle(fontSize: 11)),
                          value: _selected.contains(a.package),
                          onChanged: (v) => setState(() {
                            _touched = true;
                            v == true ? _selected.add(a.package) : _selected.remove(a.package);
                          }),
                        );
                      },
                    ),
        ),
      ]),
    );
  }
}
