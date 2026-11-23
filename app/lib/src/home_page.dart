import 'dart:io' show Platform;

import 'package:flutter/material.dart';

import 'android_bridge.dart';
import 'api.dart';
import 'country.dart';
import 'model.dart';
import 'platform_text.dart';
import 'tray.dart';
import 'update_dialog.dart';
import 'widgets.dart';

class HomePage extends StatelessWidget {
  const HomePage({super.key, required this.model, this.onPickNode});

  final AppModel model;
  final VoidCallback? onPickNode;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: SingleChildScrollView(
        padding: const EdgeInsets.all(24),
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 480),
          child: ListenableBuilder(listenable: model, builder: (context, _) => _body(context)),
        ),
      ),
    );
  }

  Widget _body(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    if (model.phase == Phase.starting) {
      return const Column(mainAxisSize: MainAxisSize.min, children: [CircularProgressIndicator(), SizedBox(height: 16), Text('Запускаю демон…')]);
    }
    if (model.phase == Phase.failed && model.killswitchHeld) {
      return Column(mainAxisSize: MainAxisSize.min, children: [
        Icon(Icons.shield_outlined, size: 48, color: cs.error),
        const SizedBox(height: 12),
        Text('Демон остановился, трафик заблокирован', style: Theme.of(context).textTheme.titleMedium, textAlign: TextAlign.center),
        const SizedBox(height: 8),
        Text(KillswitchText.current().lockedExplanation, textAlign: TextAlign.center),
        const SizedBox(height: 16),
        FilledButton.icon(onPressed: model.releaseKillswitch, icon: const Icon(Icons.lock_open), label: const Text('Снять блокировку и запустить демон')),
        const SizedBox(height: 12),
        if (KillswitchText.current().manualCommands != null)
          SelectableText(KillswitchText.current().manualCommands!, style: TextStyle(fontSize: 12, color: cs.onSurfaceVariant), textAlign: TextAlign.center),
      ]);
    }
    if (model.phase == Phase.failed) {
      return Column(mainAxisSize: MainAxisSize.min, children: [
        Icon(Icons.error_outline, size: 48, color: cs.error),
        const SizedBox(height: 12),
        Text(model.fatal ?? 'Демон недоступен', textAlign: TextAlign.center),
        const SizedBox(height: 16),
        FilledButton(onPressed: model.start, child: const Text('Повторить')),
      ]);
    }
    final vpn = model.vpn;
    final on = vpn?.on ?? false;
    final mode = vpn?.mode ?? 'blocked';
    final active = model.status?.active;
    return Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('Brod', style: Theme.of(context).textTheme.titleMedium?.copyWith(letterSpacing: 4, color: cs.onSurfaceVariant), textAlign: TextAlign.center),
      const SizedBox(height: 12),
      _PowerCard(model: model, on: on, mode: mode),
      const SizedBox(height: 20),
      // на узком экране в три сегмента слова рвутся посередине («забло-кированное»): там режимы — кнопки в столбик с целыми словами
      LayoutBuilder(
        builder: (context, c) => c.maxWidth >= 520
            ? SegmentedButton<String>(
                key: const Key('modes'),
                showSelectedIcon: false,
                segments: [for (final e in vpnModes.entries) ButtonSegment(value: e.key, label: Text(e.value, textAlign: TextAlign.center))],
                selected: {mode},
                onSelectionChanged: (s) => model.setMode(s.first),
              )
            : Wrap(
                key: const Key('modes'),
                spacing: 8,
                runSpacing: 8,
                alignment: WrapAlignment.center,
                children: [
                  for (final e in vpnModes.entries)
                    ChoiceChip(label: Text(e.value), selected: mode == e.key, showCheckmark: false, onSelected: (_) => model.setMode(e.key)),
                ],
              ),
      ),
      if (vpn != null && vpn.conflict.isNotEmpty)
        ...[
          const SizedBox(height: 16),
          _Banner(text: 'Работает другой VPN (${vpn.conflict.join(', ')}). Выключите его, иначе Brod не включится.'),
        ],
      if (Platform.isAndroid) ...[const _BatteryNote()],
      if (model.status?.update != null) ...[const SizedBox(height: 16), UpdateBanner(model: model)],
      if ((model.status?.warpEnabled ?? false) && (model.status?.warpDownMin ?? 0) >= 5) ...[const SizedBox(height: 16), _UdpNote(minutes: model.status!.warpDownMin)],
      if (!on && model.status?.netMode == 'whitelist') ...[const SizedBox(height: 16), const _WhitelistNote()],
      if ((model.notice ?? vpn?.error ?? '').isNotEmpty) ...[const SizedBox(height: 16), _Banner(text: model.notice ?? vpn!.error)],
      if (on && vpn?.killswitch == 'failed')
        ...[const SizedBox(height: 16), _Banner(text: 'Киллсвитч не включился: ${vpn!.killswitchError}')],
      const SizedBox(height: 16),
      AnimatedSwitcher(
        duration: const Duration(milliseconds: 250),
        child: model.status == null
            ? const SizedBox.shrink()
            : active == null
                ? _NoNodeCard(key: const ValueKey('none'), model: model, onPickNode: onPickNode)
                : _NodeCard(key: ValueKey(active.id), model: model, node: active),
      ),
      const SizedBox(height: 8),
      _FooterRow(model: model),
    ]);
  }
}

/// Большая круглая кнопка питания: цвет и анимация по состоянию (серая, жёлтая «дышит», зелёная, красная).
class _PowerCard extends StatefulWidget {
  const _PowerCard({required this.model, required this.on, required this.mode});

  final AppModel model;
  final bool on;
  final String mode;

  @override
  State<_PowerCard> createState() => _PowerCardState();
}

class _PowerCardState extends State<_PowerCard> with SingleTickerProviderStateMixin {
  late final AnimationController _pulse = AnimationController(vsync: this, duration: const Duration(milliseconds: 1100));

  @override
  void dispose() {
    _pulse.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    final cs = Theme.of(context).colorScheme;
    final ts = trayStateOf(m);
    final String title;
    final String sub;
    if (m.busy && m.connecting) {
      title = 'Подключаюсь…';
      sub = 'Жду рабочий выход · нажмите, чтобы прервать';
    } else if (m.busy && m.busyKind == 'mode') {
      title = 'Применяю режим…';
      sub = 'Соединения на несколько секунд оборвутся';
    } else if (m.busy) {
      title = m.busyKind == 'on' ? 'Включаю…' : 'Выключаю…';
      sub = m.busyKind == 'on' ? 'Нажмите ещё раз, чтобы прервать' : 'Соединения на несколько секунд оборвутся';
    } else if (ts == TrayState.problem) {
      title = 'VPN включён';
      sub = 'Выход не отвечает';
    } else if (widget.on) {
      title = 'VPN включён';
      sub = (vpnModes[widget.mode] ?? widget.mode) + (m.vpn?.killswitch == 'active' ? ' · киллсвитч' : '');
    } else {
      title = 'VPN выключен';
      sub = 'Трафик идёт напрямую';
    }
    final color = switch (ts) {
      TrayState.on => const Color(0xFF2EA66B),
      TrayState.connecting => const Color(0xFFE6961F),
      TrayState.problem => cs.error,
      TrayState.off => cs.outline,
    };
    if (m.busy && !_pulse.isAnimating) {
      _pulse.repeat(reverse: true);
    } else if (!m.busy && _pulse.isAnimating) {
      _pulse
        ..stop()
        ..value = 0;
    }
    return Column(children: [
      const SizedBox(height: 8),
      AnimatedBuilder(
        animation: _pulse,
        builder: (context, _) => AnimatedContainer(
          duration: const Duration(milliseconds: 400),
          curve: Curves.easeOut,
          width: 172,
          height: 172,
          alignment: Alignment.center,
          decoration: BoxDecoration(
            shape: BoxShape.circle,
            color: color.withValues(alpha: 0.10 + 0.08 * _pulse.value),
            boxShadow: ts == TrayState.off ? null : [BoxShadow(color: color.withValues(alpha: 0.30 + 0.25 * _pulse.value), blurRadius: 30 + 14 * _pulse.value, spreadRadius: 2)],
          ),
          child: AnimatedContainer(
            duration: const Duration(milliseconds: 400),
            width: 132,
            height: 132,
            decoration: BoxDecoration(shape: BoxShape.circle, color: color, boxShadow: const [BoxShadow(color: Colors.black26, blurRadius: 8, offset: Offset(0, 3))]),
            child: Material(
              key: const Key('power'),
              color: Colors.transparent,
              shape: const CircleBorder(),
              child: InkWell(
                customBorder: const CircleBorder(),
                onTap: m.busy && m.busyKind != 'on' ? null : () => m.setOn(!widget.on),
                child: Center(
                  child: m.busy && m.busyKind == 'on'
                      ? const Stack(alignment: Alignment.center, children: [SizedBox(width: 88, height: 88, child: CircularProgressIndicator(strokeWidth: 4, color: Colors.white)), Icon(Icons.stop_rounded, size: 40, color: Colors.white)])
                      : m.busy
                          ? const SizedBox(width: 42, height: 42, child: CircularProgressIndicator(strokeWidth: 4, color: Colors.white))
                          : const Icon(Icons.power_settings_new, size: 66, color: Colors.white),
                ),
              ),
            ),
          ),
        ),
      ),
      const SizedBox(height: 14),
      AnimatedSwitcher(duration: const Duration(milliseconds: 200), child: Text(title, key: ValueKey(title), style: Theme.of(context).textTheme.titleLarge)),
      const SizedBox(height: 2),
      Text(sub, textAlign: TextAlign.center, style: Theme.of(context).textTheme.bodyMedium?.copyWith(color: cs.onSurfaceVariant)),
    ]);
  }
}

class _Banner extends StatelessWidget {
  const _Banner({required this.text});

  final String text;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Card.filled(
      color: cs.errorContainer,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Icon(Icons.warning_amber, color: cs.onErrorContainer),
          const SizedBox(width: 10),
          Expanded(child: Text(text, style: TextStyle(color: cs.onErrorContainer))),
        ]),
      ),
    );
  }
}

class _BatteryNote extends StatefulWidget {
  const _BatteryNote();

  @override
  State<_BatteryNote> createState() => _BatteryNoteState();
}

class _BatteryNoteState extends State<_BatteryNote> {
  bool? ignored;
  bool hidden = true; // пока не знаем, не показываем: плашка не должна мигать

  @override
  void initState() {
    super.initState();
    AndroidCore.batteryIgnored().then((v) {
      if (mounted) setState(() => ignored = v);
    });
    AndroidCore.batteryNoteHidden().then((v) {
      if (mounted) setState(() => hidden = v);
    });
  }

  @override
  Widget build(BuildContext context) {
    if (ignored != false || hidden) return const SizedBox.shrink();
    final cs = Theme.of(context).colorScheme;
    return Padding(
      padding: const EdgeInsets.only(top: 16),
      child: Card.filled(
        color: cs.secondaryContainer,
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Icon(Icons.battery_alert, color: cs.onSecondaryContainer),
              const SizedBox(width: 10),
              Expanded(child: Text('Оптимизация батареи отключает сеть Brod при выключенном экране: проверки узлов и WARP не работают', style: TextStyle(color: cs.onSecondaryContainer))),
            ]),
            const SizedBox(height: 8),
            Row(mainAxisAlignment: MainAxisAlignment.end, children: [
              // на HyperOS «Без ограничений» в системном окне не всегда меняет флаг, который видит приложение: плашку можно убрать насовсем
              TextButton(
                onPressed: () async {
                  await AndroidCore.hideBatteryNote();
                  if (mounted) setState(() => hidden = true);
                },
                child: const Text('Уже разрешено'),
              ),
              const SizedBox(width: 8),
              FilledButton.tonal(
                onPressed: () async {
                  final v = await AndroidCore.requestIgnoreBattery();
                  if (mounted) setState(() => ignored = v);
                },
                child: const Text('Разрешить'),
              ),
            ]),
          ]),
        ),
      ),
    );
  }
}

/// Оператор пропускает только разрешённые сайты (белые списки): обычные узлы, скорее всего, не заработают.
/// WARP не отвечает уже несколько минут, а интернет есть: сеть, похоже, режет UDP (WARP и WireGuard идут по UDP). Живых узлов будет мало.
class _UdpNote extends StatelessWidget {
  const _UdpNote({required this.minutes});

  final int minutes;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Card.filled(
      color: cs.secondaryContainer,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Icon(Icons.wifi_off, color: cs.onSecondaryContainer),
          const SizedBox(width: 10),
          Expanded(
              child: Text(
                  'WARP не отвечает уже $minutes мин. Похоже, эта сеть не пропускает UDP: живых узлов будет очень мало. Попробуйте другую сеть, например мобильную, или проверьте защиту от UDP-флуда в роутере.',
                  style: TextStyle(color: cs.onSecondaryContainer))),
        ]),
      ),
    );
  }
}

class _WhitelistNote extends StatelessWidget {
  const _WhitelistNote();

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Card.filled(
      color: cs.secondaryContainer,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Icon(Icons.info_outline, color: cs.onSecondaryContainer),
          const SizedBox(width: 10),
          Expanded(
              child: Text(
                  'Оператор пропускает только сайты из белых списков. Brod проверяет узлы с адресами из этих списков; остальные, скорее всего, не заработают.',
                  style: TextStyle(color: cs.onSecondaryContainer))),
        ]),
      ),
    );
  }
}

/// Карточка узла не выбран: предложить лучший или выбрать вручную.
class _NoNodeCard extends StatelessWidget {
  const _NoNodeCard({super.key, required this.model, this.onPickNode});

  final AppModel model;
  final VoidCallback? onPickNode;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Card.filled(
      color: cs.tertiaryContainer,
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            Icon(Icons.info_outline, color: cs.onTertiaryContainer),
            const SizedBox(width: 10),
            Expanded(child: Text('Узел не выбран', style: Theme.of(context).textTheme.titleSmall?.copyWith(color: cs.onTertiaryContainer))),
          ]),
          const SizedBox(height: 6),
          Text('Без него заблокированное не откроется.', style: TextStyle(color: cs.onTertiaryContainer)),
          const SizedBox(height: 12),
          Wrap(spacing: 8, runSpacing: 4, crossAxisAlignment: WrapCrossAlignment.center, children: [
            FilledButton.icon(
              onPressed: model.bestBusy ? null : model.connectBest,
              icon: model.bestBusy ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.auto_awesome),
              label: const Text('Подключиться к лучшему узлу'),
            ),
            TextButton(onPressed: onPickNode, child: const Text('Выбрать вручную')),
          ]),
        ]),
      ),
    );
  }
}

/// Карточка активного узла: флаг, название, пинг/скорость/чистота, действия.
class _NodeCard extends StatelessWidget {
  const _NodeCard({super.key, required this.model, required this.node});

  final AppModel model;
  final ActiveNode node;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final warp = node.proto == 'warp';
    final p = model.probe;
    final exitOk = p?.ok;
    final dto = NodeDto(
      id: node.id,
      proto: node.proto,
      server: node.server,
      port: node.port,
      latencyMs: node.latencyMs,
      speedKBps: node.speedKBps,
      udp: node.udp,
      youtube: node.youtube,
      insecure: node.insecure,
      own: node.own,
      exit: node.clean.isEmpty && node.country.isEmpty ? null : ExitDto(ip: node.ip, country: node.country, clean: node.clean),
    );
    return Card.filled(
      color: cs.surfaceContainerHigh,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 14, 8, 8),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            warp
                ? Container(width: 44, height: 44, decoration: BoxDecoration(shape: BoxShape.circle, color: cs.surfaceContainerHighest), child: Icon(Icons.cloud_outlined, color: cs.primary))
                : FlagBadge(country: node.country),
            const SizedBox(width: 12),
            Expanded(
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Text(node.title, style: Theme.of(context).textTheme.titleMedium, overflow: TextOverflow.ellipsis),
                Text(
                  warp ? 'Встроенный выход' : (node.country.isEmpty ? 'страна определяется…' : '${countryName(node.country)}${node.ip.isEmpty ? '' : ' · ${node.ip}'}'),
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(color: cs.onSurfaceVariant),
                  overflow: TextOverflow.ellipsis,
                ),
              ]),
            ),
            PopupMenuButton<String>(
              tooltip: 'Действия',
              icon: const Icon(Icons.more_vert),
              onSelected: (k) {
                switch (k) {
                  case 'probe':
                    model.checkExit();
                  case 'ping':
                    model.startMeasure(ping: true, ids: [dto.id]);
                  case 'speed':
                    model.measureSpeed(dto);
                  case 'calls':
                    model.probeCalls(dto);
                  case 'exit':
                    model.detectExit(dto);
                  case 'share':
                    model.shareNode(dto);
                  case 'best':
                    model.connectBest();
                }
              },
              itemBuilder: (_) => [
                const PopupMenuItem(value: 'probe', child: ListTile(dense: true, leading: Icon(Icons.network_check), title: Text('Проверить выход'))),
                // те же проверки, что в меню узла на вкладке «Узлы», — для активного узла без перехода туда (у WARP узла в базе нет)
                if (!warp) ...const [
                  PopupMenuItem(value: 'ping', child: ListTile(dense: true, leading: Icon(Icons.network_ping), title: Text('Проверить пинг'))),
                  PopupMenuItem(value: 'speed', child: ListTile(dense: true, leading: Icon(Icons.speed), title: Text('Замерить скорость'))),
                  PopupMenuItem(value: 'calls', child: ListTile(dense: true, leading: Icon(Icons.headset_mic_outlined), title: Text('Проверить для звонков'))),
                  PopupMenuItem(value: 'exit', child: ListTile(dense: true, leading: Icon(Icons.travel_explore), title: Text('Проверить страну и чистоту IP'))),
                ],
                if (!warp) const PopupMenuItem(value: 'share', child: ListTile(dense: true, leading: Icon(Icons.share_outlined), title: Text('Поделиться ссылкой'))),
                const PopupMenuItem(value: 'best', child: ListTile(dense: true, leading: Icon(Icons.auto_awesome), title: Text('Подключиться к лучшему узлу'))),
              ],
            ),
          ]),
          const SizedBox(height: 10),
          Padding(
            padding: const EdgeInsets.only(right: 8),
            child: Wrap(spacing: 6, runSpacing: 4, crossAxisAlignment: WrapCrossAlignment.center, children: [
              if (!warp)
                NodePills(node: dto, showPing: p == null, leading: [if (p != null) _exitPill(p, exitOk == true, cs)])
              else if (p != null)
                _exitPill(p, exitOk == true, cs),
              if (model.status?.degraded == true) Pill(text: 'деградирует', icon: Icons.warning_amber, color: cs.error, filled: true),
            ]),
          ),
          const SizedBox(height: 4),
        ]),
      ),
    );
  }
}

/// Одна таблетка вместо двух («пинг» и «выход работает»): число — задержка живой проверки выхода, цвет — по порогам пинга.
Widget _exitPill(ProbeResult p, bool ok, ColorScheme cs) => ok
    ? Pill(text: 'выход работает · ${p.latencyMs} мс', icon: Icons.check_circle_outline, color: pingColor(p.latencyMs, cs), filled: true)
    : Pill(text: 'выход не отвечает', icon: Icons.error_outline, color: cs.error, filled: true);

class _FooterRow extends StatelessWidget {
  const _FooterRow({required this.model});

  final AppModel model;

  @override
  Widget build(BuildContext context) {
    final st = model.status;
    if (st == null) return const SizedBox.shrink();
    return Text('Узлов в базе: ${st.nodes} · в источниках: ${st.present} · brodd ${st.version}', textAlign: TextAlign.center, style: Theme.of(context).textTheme.labelSmall?.copyWith(color: Theme.of(context).colorScheme.outline));
  }
}
