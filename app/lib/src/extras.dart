import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';

import 'api.dart';
import 'country.dart';
import 'model.dart';
import 'platform_paths.dart';
import 'tray.dart';

// ---------------- фон-«свечение» по состоянию ----------------

/// Мягкое свечение сверху окна цвета состояния VPN (серое — нет, зелёное — подключён, жёлтое — подключаюсь, красное — проблема).
class StateGlow extends StatelessWidget {
  const StateGlow({super.key, required this.model, required this.child});

  final AppModel model;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final color = switch (trayStateOf(model)) {
          TrayState.on => const Color(0xFF2EA66B),
          TrayState.connecting => const Color(0xFFE6961F),
          TrayState.problem => cs.error,
          TrayState.off => cs.primary,
        };
        final strength = trayStateOf(model) == TrayState.off ? 0.05 : 0.16;
        return Stack(children: [
          Positioned.fill(
            child: IgnorePointer(
              // радиус RadialGradient считается от меньшей стороны, поэтому на узком высоком экране (телефон) свечение выходило втрое короче, чем в
              // окне рабочего стола: доводим его до той же доли высоты (на широком окне радиус остаётся прежним, 1.0)
              child: LayoutBuilder(
                builder: (context, c) {
                  final radius = c.biggest.shortestSide > 0 ? (0.63 * c.biggest.height / c.biggest.shortestSide).clamp(1.0, 1.9) : 1.0;
                  return AnimatedContainer(
                    duration: const Duration(milliseconds: 600),
                    decoration: BoxDecoration(gradient: RadialGradient(center: const Alignment(0, -1.15), radius: radius.toDouble(), colors: [color.withValues(alpha: strength), color.withValues(alpha: 0)])),
                  );
                },
              ),
            ),
          ),
          child,
        ]);
      },
    );
  }
}

// ---------------- диагностика ----------------

/// «Что видят сайты»: внешний адрес, страна, IPv6, DNS-резолвер и выводы.
class DiagnosticsPage extends StatefulWidget {
  const DiagnosticsPage({super.key, required this.model});

  final AppModel model;

  @override
  State<DiagnosticsPage> createState() => _DiagnosticsPageState();
}

class _DiagnosticsPageState extends State<DiagnosticsPage> {
  @override
  void initState() {
    super.initState();
    Future.microtask(widget.model.runDiagnostics);
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final d = m.diag;
        final cs = Theme.of(context).colorScheme;
        return Scaffold(
          appBar: AppBar(
            title: const Text('Диагностика'),
            actions: [IconButton(tooltip: 'Проверить заново', icon: const Icon(Icons.refresh), onPressed: m.diagRunning ? null : m.runDiagnostics)],
          ),
          body: Column(children: [
            if (m.diagRunning) const LinearProgressIndicator(minHeight: 2),
            Expanded(
              child: d == null
                  ? const Center(child: Text('Проверяю…'))
                  : ListView(padding: const EdgeInsets.all(16), children: [
                      Card.filled(
                        child: Padding(
                          padding: const EdgeInsets.all(16),
                          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                            Text('Что видят сайты', style: Theme.of(context).textTheme.titleSmall),
                            const SizedBox(height: 10),
                            _kv(context, 'Внешний адрес', d.ip.isEmpty ? 'нет ответа' : d.ip),
                            _kv(context, 'Страна', d.country.isEmpty ? '—' : '${flagOf(d.country)} ${countryName(d.country)}${d.colo.isEmpty ? '' : ' · ${d.colo}'}'),
                            _kv(context, 'IPv6', d.ipv6.isEmpty ? 'недоступен' : d.ipv6),
                            _kv(context, 'DNS-резолвер', d.resolver.isEmpty ? '—' : d.resolver),
                            _kv(context, 'VPN', d.vpnOn ? 'включён · ${vpnModes[d.mode] ?? d.mode}' : 'выключен'),
                          ]),
                        ),
                      ),
                      if (d.geo.isNotEmpty) ...[
                        const SizedBox(height: 8),
                        Card.filled(
                          child: Padding(
                            padding: const EdgeInsets.all(16),
                            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                              Text('Что говорят базы геолокации', style: Theme.of(context).textTheme.titleSmall),
                              const SizedBox(height: 4),
                              Text('Сайты берут страну и тип адреса из своих баз, а базы между собой расходятся.', style: Theme.of(context).textTheme.bodySmall),
                              const SizedBox(height: 8),
                              for (final g in d.geo) _geoRow(context, g),
                            ]),
                          ),
                        ),
                      ],
                      const SizedBox(height: 8),
                      for (final f in d.findings)
                        Card.outlined(
                          child: ListTile(
                            leading: Icon(
                              switch (f.level) { 'ok' => Icons.check_circle_outline, 'warn' => Icons.warning_amber, 'bad' => Icons.error_outline, _ => Icons.info_outline },
                              color: switch (f.level) { 'ok' => Colors.green, 'warn' => Colors.orange, 'bad' => cs.error, _ => cs.primary },
                            ),
                            title: Text(f.title),
                            subtitle: Text(f.detail),
                          ),
                        ),
                    ]),
            ),
          ]),
        );
      },
    );
  }

  Widget _geoRow(BuildContext context, GeoDto g) {
    final cs = Theme.of(context).colorScheme;
    final small = Theme.of(context).textTheme.bodySmall;
    final tags = [
      if (g.hosting == true) 'дата-центр',
      if (g.proxy == true) 'прокси/VPN',
    ];
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
        SizedBox(width: 84, child: Text(g.name, style: small)),
        Expanded(
          child: g.error.isNotEmpty
              ? Text('нет ответа (${g.error})', style: TextStyle(color: cs.outline))
              : Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text('${flagOf(g.country)} ${countryName(g.country)}${g.city.isEmpty ? '' : ' · ${g.city}'}'),
                  if (g.org.isNotEmpty || g.timezone.isNotEmpty) Text([g.org, g.timezone].where((e) => e.isNotEmpty).join(' · '), style: small),
                  if (tags.isNotEmpty) Text(tags.join(', '), style: TextStyle(color: cs.error, fontSize: 12)),
                ]),
        ),
      ]),
    );
  }

  Widget _kv(BuildContext context, String k, String v) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 3),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          SizedBox(width: 120, child: Text(k, style: Theme.of(context).textTheme.bodySmall)),
          Expanded(child: SelectableText(v)),
        ]),
      );
}

// ---------------- резервная копия ----------------

String get _backupDir => userHomePath(Platform.environment, windows: Platform.isWindows);

/// Путь для копии: ~/brod-backup-ГГГГ-ММ-ДД.json (на Windows — в %USERPROFILE%).
String defaultBackupPath() {
  final n = DateTime.now();
  String two(int v) => v.toString().padLeft(2, '0');
  return '$_backupDir${Platform.pathSeparator}brod-backup-${n.year}-${two(n.month)}-${two(n.day)}.json';
}

/// Последняя копия в домашнем каталоге (для подсказки при восстановлении).
String? latestBackupPath() {
  try {
    final files = Directory(_backupDir).listSync().whereType<File>().where((f) => RegExp(r'brod-backup-.*\.json$').hasMatch(f.path)).toList()
      ..sort((a, b) => a.path.compareTo(b.path));
    return files.isEmpty ? null : files.last.path;
  } catch (_) {
    return null;
  }
}

Future<void> backupDialog(BuildContext context, AppModel m, {required bool restore}) async {
  if (Platform.isAndroid) {
    // на Android путь к файлу пользователь не вводит: системное окно выбора места сохранения или файла
    if (restore) {
      await m.restoreBackupAndroid(confirm: (summary) => confirmRestore(context, summary));
    } else {
      await m.saveBackupAndroid();
    }
    return;
  }
  final c = TextEditingController(text: restore ? (latestBackupPath() ?? defaultBackupPath()) : defaultBackupPath());
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: Text(restore ? 'Восстановить из копии' : 'Сохранить копию'),
      content: SizedBox(
        width: 460,
        child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
          TextField(controller: c, decoration: const InputDecoration(labelText: 'Файл')),
          const SizedBox(height: 10),
          Text(
            restore
                ? 'Настройки и правила заменятся, свои серверы добавятся.'
                : 'В копии есть пароли и ключи серверов: храните файл осторожно.',
            style: Theme.of(ctx).textTheme.bodySmall,
          ),
        ]),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
        FilledButton(onPressed: () => Navigator.pop(ctx, true), child: Text(restore ? 'Восстановить' : 'Сохранить')),
      ],
    ),
  );
  if (ok != true) return;
  if (restore) {
    if (!context.mounted) return;
    await m.restoreBackup(c.text.trim(), confirm: (summary) => confirmRestore(context, summary));
  } else {
    await m.saveBackup(c.text.trim());
  }
}

/// Подтверждение перед применением копии: показывает, что она изменит. Копию из чужих рук восстанавливать нельзя.
@visibleForTesting
Future<bool> confirmRestore(BuildContext context, String summary) async {
  if (!context.mounted) return false;
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: const Text('Копия изменит'),
      content: SizedBox(
        width: 460,
        child: SingleChildScrollView(
          child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
            SelectableText(summary, style: Theme.of(ctx).textTheme.bodySmall),
            const SizedBox(height: 12),
            Text(
              'Восстанавливайте только свою копию: чужая может выключить киллсвитч и подменить источники и серверы.',
              style: Theme.of(ctx).textTheme.bodySmall,
            ),
          ]),
        ),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
        FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Применить')),
      ],
    ),
  );
  return ok == true;
}

// ---------------- аккаунт WARP ----------------

/// Регистрация нового аккаунта WARP; если он уже есть, сначала спрашивает, заменять ли (прежний ключ пропадёт).
Future<void> warpRegisterDialog(BuildContext context, AppModel m) async {
  final have = m.status?.warpEnabled == true;
  if (have) {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Заменить аккаунт WARP?'),
        content: const Text('Будет создан новый аккаунт, прежний ключ пропадёт.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Заменить')),
        ],
      ),
    );
    if (ok != true) return;
  }
  await m.warpRegister(replace: have);
}

/// Загрузка готового аккаунта WARP из файла (на Android — системный выбор файла, на ПК — путь).
Future<void> warpImportDialog(BuildContext context, AppModel m) async {
  final have = m.status?.warpEnabled == true;
  if (Platform.isAndroid) {
    await m.warpImportAndroid(replace: have);
    return;
  }
  final c = TextEditingController(text: '${Platform.environment['HOME'] ?? Platform.environment['USERPROFILE'] ?? '.'}${Platform.pathSeparator}warp-account.json');
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: const Text('Аккаунт WARP из файла'),
      content: SizedBox(
        width: 460,
        child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
          TextField(controller: c, decoration: const InputDecoration(labelText: 'Файл warp-account.json')),
          const SizedBox(height: 10),
          Text(have ? 'Прежний аккаунт заменится.' : 'В файле приватный ключ: храните его осторожно.', style: Theme.of(ctx).textTheme.bodySmall),
        ]),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Отмена')),
        FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Загрузить')),
      ],
    ),
  );
  if (ok == true) await m.warpImportFile(c.text.trim(), replace: have);
}

// ---------------- шаблоны правил ----------------

/// Готовый набор правил маршрутизации, добавляемый одной кнопкой.
class RulePreset {
  const RulePreset(this.title, this.subtitle, this.icon, this.action, this.suffixes, {this.tlsSpoof = '', this.desktopOnly = false});

  final String title;
  final String subtitle;
  final IconData icon;
  final String action; // proxy | direct
  final List<String> suffixes;
  final String tlsSpoof; // имя сайта для подмены SNI ('' — без подмены)
  final bool desktopOnly; // не показывать на Android
}

const rulePresets = <RulePreset>[
  RulePreset('Мессенджеры', 'Telegram, WhatsApp, Discord, Signal — через VPN', Icons.chat_bubble_outline, 'proxy', [
    'telegram.org', 't.me', 'telegram.me', 'telesco.pe', 'whatsapp.com', 'whatsapp.net', 'discord.com', 'discord.gg', 'discordapp.com', 'discord.media', 'signal.org', 'signal.art',
  ]),
  RulePreset('Соцсети', 'Instagram, Facebook, X, LinkedIn — через VPN', Icons.groups_outlined, 'proxy', [
    'instagram.com', 'cdninstagram.com', 'facebook.com', 'fbcdn.net', 'twitter.com', 'x.com', 'twimg.com', 't.co', 'linkedin.com', 'licdn.com',
  ]),
  RulePreset('Видео и музыка', 'YouTube, Netflix, Spotify, Twitch — через VPN', Icons.play_circle_outline, 'proxy', [
    'youtube.com', 'youtu.be', 'ytimg.com', 'googlevideo.com', 'netflix.com', 'nflxvideo.net', 'nflximg.net', 'spotify.com', 'scdn.co', 'twitch.tv', 'ttvnw.net', 'disneyplus.com',
  ]),
  RulePreset('Игры', 'Steam, Epic, Battle.net, Riot — через VPN', Icons.sports_esports_outlined, 'proxy', [
    'steampowered.com', 'steamcommunity.com', 'steamstatic.com', 'epicgames.com', 'unrealengine.com', 'battle.net', 'blizzard.com', 'riotgames.com', 'leagueoflegends.com',
  ]),
  RulePreset('ИИ-сервисы', 'ChatGPT, Claude, Gemini, Perplexity — через VPN', Icons.auto_awesome_outlined, 'proxy', [
    'openai.com', 'chatgpt.com', 'oaistatic.com', 'oaiusercontent.com', 'anthropic.com', 'claude.ai', 'gemini.google.com', 'aistudio.google.com', 'perplexity.ai', 'huggingface.co',
  ]),
  RulePreset('Российское напрямую', 'Яндекс, VK, Ozon, Wildberries, Авито — всегда напрямую', Icons.home_outlined, 'direct', [
    'yandex.ru', 'yandex.net', 'ya.ru', 'vk.com', 'vk.ru', 'userapi.com', 'mail.ru', 'ok.ru', 'ozon.ru', 'wildberries.ru', 'wb.ru', 'avito.ru', 'sber.ru', 'sberbank.ru', 'tinkoff.ru', 'gosuslugi.ru', 'rutube.ru',
  ]),
  // проверено на одном домашнем провайдере; у других провайдеров может не помочь
  RulePreset('Обход блокировки по SNI', 'Discord, X, Instagram напрямую с подменой SNI (Linux и Windows)', Icons.shield_outlined, 'direct', [
    'discord.com', 'discord.gg', 'discordapp.com', 'discord.media', 'x.com', 'twitter.com', 'twimg.com', 'instagram.com', 'cdninstagram.com',
  ], tlsSpoof: 'www.cloudflare.com', desktopOnly: true),
];
