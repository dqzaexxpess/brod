import 'package:flutter/material.dart';

import 'api.dart';
import 'extras.dart';
import 'model.dart';

/// Окно «Cloudflare WARP»: состояние, подбор профиля под сеть, ручные параметры, перегенерация аккаунта.
class WarpPage extends StatefulWidget {
  const WarpPage({super.key, required this.model});

  final AppModel model;

  @override
  State<WarpPage> createState() => _WarpPageState();
}

const _masks = ['quic', 'dns', 'stun', 'sip', 'none'];
const _browsers = ['', 'chrome', 'firefox', 'curl'];

class _WarpPageState extends State<WarpPage> {
  final endpoint = TextEditingController();
  final port = TextEditingController();
  final mtu = TextEditingController();
  final maskHost = TextEditingController();
  String mask = '';
  String browser = '';
  bool loaded = false;

  AppModel get m => widget.model;

  @override
  void initState() {
    super.initState();
    m.loadWarp().then((_) => _fill());
  }

  @override
  void dispose() {
    for (final c in [endpoint, port, mtu, maskHost]) {
      c.dispose();
    }
    super.dispose();
  }

  void _fill() {
    final s = m.warpState;
    if (s == null || !mounted) return;
    setState(() {
      endpoint.text = s.manual['warp.endpoint'] ?? '';
      port.text = s.manual['warp.port'] ?? '';
      mtu.text = s.manual['warp.mtu'] ?? '';
      maskHost.text = s.manual['warp.mask_host'] ?? '';
      mask = s.manual['warp.mask'] ?? '';
      browser = s.manual['warp.browser'] ?? '';
      loaded = true;
    });
  }

  Map<String, String> _params() => {
        'warp.endpoint': endpoint.text.trim(),
        'warp.port': port.text.trim(),
        'warp.mask': mask,
        'warp.mtu': mtu.text.trim(),
        'warp.mask_host': maskHost.text.trim(),
        'warp.browser': browser,
      };

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final s = m.warpState;
        final cs = Theme.of(context).colorScheme;
        final small = Theme.of(context).textTheme.bodySmall;
        return Scaffold(
          appBar: AppBar(title: const Text('Cloudflare WARP')),
          body: s == null
              ? const Center(child: CircularProgressIndicator())
              : ListView(padding: const EdgeInsets.all(16), children: [
                  Card.filled(
                    child: Padding(
                      padding: const EdgeInsets.all(14),
                      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                        Row(children: [
                          Icon(s.enabled ? (s.down ? Icons.cloud_off : Icons.cloud_done_outlined) : Icons.cloud_outlined, color: s.down ? cs.error : cs.primary),
                          const SizedBox(width: 10),
                          Expanded(child: Text(!s.enabled ? 'Аккаунт не настроен' : (s.down ? 'WARP не отвечает, цепочка выключена' : 'Аккаунт настроен'), style: Theme.of(context).textTheme.titleMedium)),
                        ]),
                        if (s.enabled) ...[
                          const SizedBox(height: 8),
                          Text('Сейчас используется: ${s.current ?? '—'}'),
                          const SizedBox(height: 4),
                          Text(s.remembered == null ? 'Для этой сети вариант ещё не подбирался' : 'Запомнено для этой сети: ${s.remembered}', style: small),
                        ],
                      ]),
                    ),
                  ),
                  const SizedBox(height: 8),
                  SwitchListTile(
                    contentPadding: EdgeInsets.zero,
                    title: const Text('Подбирать под сеть сам'),
                    subtitle: const Text('При сбое WARP в новой сети пробуются другие адреса, порты и маскировки'),
                    value: s.auto,
                    onChanged: s.enabled ? m.setWarpAuto : null,
                  ),
                  const SizedBox(height: 8),
                  FilledButton.icon(
                    icon: m.warpScanning || s.scanning ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.travel_explore),
                    label: Text(m.warpScanning || s.scanning ? 'Подбираю… (до нескольких минут)' : 'Подобрать для этой сети'),
                    onPressed: !s.enabled || m.warpScanning || s.scanning ? null : m.scanWarp,
                  ),
                  if (m.warpScanResults != null) ..._results(context, m.warpScanResults!),
                  const SizedBox(height: 20),
                  Text('Параметры вручную', style: Theme.of(context).textTheme.titleMedium),
                  const SizedBox(height: 4),
                  Text('Пустое поле — по умолчанию. «Применить» перезапускает VPN', style: small),
                  const SizedBox(height: 8),
                  _field(endpoint, 'Адрес', hint: 'engage.cloudflareclient.com или 188.114.96.74'),
                  Row(children: [
                    Expanded(child: _field(port, 'Порт', hint: '2408', number: true)),
                    const SizedBox(width: 12),
                    Expanded(child: _field(mtu, 'MTU', hint: '1280', number: true)),
                  ]),
                  DropdownButtonFormField<String>(
                    // ignore: deprecated_member_use
                    value: _masks.contains(mask) ? mask : null,
                    decoration: const InputDecoration(labelText: 'Маскировка'),
                    items: [for (final x in _masks) DropdownMenuItem(value: x, child: Text(x == 'none' ? 'нет (обычный WireGuard)' : x))],
                    onChanged: (v) => setState(() => mask = v ?? ''),
                  ),
                  const SizedBox(height: 12),
                  _field(maskHost, 'Домен маскировки', hint: 'www.google.com'),
                  DropdownButtonFormField<String>(
                    // ignore: deprecated_member_use
                    value: browser,
                    decoration: const InputDecoration(labelText: 'Браузер для маскировки quic'),
                    items: [for (final x in _browsers) DropdownMenuItem(value: x, child: Text(x.isEmpty ? 'по умолчанию' : x))],
                    onChanged: (v) => setState(() => browser = v ?? ''),
                  ),
                  const SizedBox(height: 12),
                  Row(children: [
                    FilledButton(onPressed: loaded && s.enabled ? () async { await m.applyWarpParams(_params()); _fill(); } : null, child: const Text('Применить')),
                    const SizedBox(width: 12),
                    OutlinedButton(onPressed: s.enabled ? () async { await m.resetWarp(); _fill(); } : null, child: const Text('Сбросить к умолчанию')),
                  ]),
                  const SizedBox(height: 24),
                  Text('Аккаунт', style: Theme.of(context).textTheme.titleMedium),
                  ListTile(
                    contentPadding: EdgeInsets.zero,
                    leading: m.warpBusy ? const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.autorenew),
                    title: const Text('Перегенерировать аккаунт'),
                    subtitle: const Text('Новые ключи; если сеть режет, регистрация пойдёт через выбранный узел'),
                    enabled: !m.warpBusy,
                    onTap: () => warpRegisterDialog(context, m),
                  ),
                  ListTile(
                    contentPadding: EdgeInsets.zero,
                    leading: const Icon(Icons.file_open_outlined),
                    title: const Text('Загрузить аккаунт из файла'),
                    subtitle: const Text('warp-account.json с другого устройства'),
                    enabled: !m.warpBusy,
                    onTap: () => warpImportDialog(context, m),
                  ),
                ]),
        );
      },
    );
  }

  List<Widget> _results(BuildContext context, List<WarpScanResultDto> res) {
    final ok = res.where((r) => r.ok).toList()..sort((a, b) => a.latencyMs.compareTo(b.latencyMs));
    final tried = res.where((r) => r.cls != 'skipped').length;
    return [
      const SizedBox(height: 12),
      Text(ok.isEmpty ? 'Проверено вариантов: $tried, рабочих нет.' : 'Проверено вариантов: $tried, рабочих: ${ok.length}. Лучшие:', style: Theme.of(context).textTheme.titleSmall),
      for (final r in ok.take(5))
        ListTile(dense: true, contentPadding: EdgeInsets.zero, leading: const Icon(Icons.check_circle_outline, color: Colors.green), title: Text('${r.profile}'), trailing: Text('${r.latencyMs} мс')),
    ];
  }

  Widget _field(TextEditingController c, String label, {String? hint, bool number = false}) => Padding(
        padding: const EdgeInsets.only(bottom: 12),
        child: TextField(controller: c, keyboardType: number ? TextInputType.number : TextInputType.text, decoration: InputDecoration(labelText: label, hintText: hint)),
      );
}
