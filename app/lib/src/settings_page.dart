import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'autostart.dart';
import 'extras.dart';
import 'android_bridge.dart';
import 'apps_page.dart';
import 'model.dart';
import 'platform_text.dart';
import 'own_page.dart';
import 'prefs.dart';
import 'report_page.dart';
import 'rules_page.dart';
import 'sources_page.dart';
import 'user_sets_page.dart';
import 'warp_page.dart';
import 'update_dialog.dart';

/// Разделы настроек по смыслу. На узком экране (телефон) список разделов, каждый открывается отдельным экраном; на широком окне (компьютер)
/// список слева, содержимое выбранного раздела справа.
enum _Sec { connection, routing, nodes, notify, look, system, data, about }

class _SecInfo {
  const _SecInfo(this.icon, this.title);
  final IconData icon;
  final String title;
}

const _secInfo = {
  _Sec.connection: _SecInfo(Icons.vpn_key_outlined, 'Подключение'),
  _Sec.routing: _SecInfo(Icons.alt_route, 'Маршрутизация'),
  _Sec.nodes: _SecInfo(Icons.dns_outlined, 'Узлы и источники'),
  _Sec.notify: _SecInfo(Icons.notifications_outlined, 'Уведомления'),
  _Sec.look: _SecInfo(Icons.palette_outlined, 'Оформление'),
  _Sec.system: _SecInfo(Icons.desktop_windows_outlined, 'Запуск и окно'),
  _Sec.data: _SecInfo(Icons.folder_outlined, 'Данные и диагностика'),
  _Sec.about: _SecInfo(Icons.info_outline, 'О приложении и обновление'),
};

/// Настройки.
class SettingsPage extends StatefulWidget {
  const SettingsPage({super.key, required this.model, this.visible = true, this.prefs, this.onQuit});

  final AppModel model;
  final bool visible;
  final Prefs? prefs;
  final VoidCallback? onQuit;

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  bool? _autostart; // null — ещё не прочитан
  String? _autostartError;
  final _tick = ValueNotifier<int>(0); // перерисовка открытого раздела при изменении локального состояния
  _Sec _sel = _Sec.connection;

  /// Разделы, которые есть на этой платформе и при этих данных.
  List<_Sec> get _secs => [
        _Sec.connection,
        _Sec.routing,
        _Sec.nodes,
        _Sec.notify,
        _Sec.look,
        if (_hasSystem) _Sec.system,
        _Sec.data,
        _Sec.about,
      ];

  bool get _hasSystem => Autostart.supported || (!Platform.isAndroid && widget.prefs != null);

  @override
  void initState() {
    super.initState();
    if (widget.visible) Future.microtask(widget.model.loadSettings);
    if (Autostart.supported) {
      Autostart.enabled().then((v) {
        if (mounted) setState(() => _autostart = v);
        _tick.value++;
      });
    }
  }

  Future<void> _setAutostart(bool on) async {
    final err = await Autostart.set(on);
    final now = await Autostart.enabled();
    if (mounted) {
      setState(() {
        _autostart = now;
        _autostartError = err;
      });
    }
    _tick.value++;
  }

  @override
  void didUpdateWidget(SettingsPage old) {
    super.didUpdateWidget(old);
    if (widget.visible && !old.visible) Future.microtask(widget.model.loadSettings);
  }

  @override
  void dispose() {
    _tick.dispose();
    super.dispose();
  }

  Listenable get _changes => Listenable.merge([widget.model, _tick, if (widget.prefs != null) widget.prefs!]);

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return LayoutBuilder(builder: (context, box) {
      final wide = box.maxWidth >= 840;
      return ListenableBuilder(
        listenable: m,
        builder: (context, _) {
          final secs = _secs;
          if (!secs.contains(_sel)) _sel = secs.first;
          final list = ListView(padding: const EdgeInsets.all(8), children: [
            // место под полосу занято всегда: если вставлять её только на время сохранения, список при прокрутке дёргается на её высоту
            SizedBox(height: 2, child: m.settingsLoading ? const LinearProgressIndicator(minHeight: 2) : null),
            for (final sec in secs)
              ListTile(
                leading: Icon(_secInfo[sec]!.icon),
                title: Text(_secInfo[sec]!.title),
                subtitle: Text(_summary(sec, m), maxLines: 2, overflow: TextOverflow.ellipsis),
                trailing: wide ? null : const Icon(Icons.chevron_right),
                selected: wide && sec == _sel,
                selectedTileColor: Theme.of(context).colorScheme.secondaryContainer.withValues(alpha: 0.5),
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                onTap: () {
                  if (wide) {
                    setState(() => _sel = sec);
                  } else {
                    Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => _SectionScreen(title: _secInfo[sec]!.title, changes: _changes, builder: (c) => _section(c, sec))));
                  }
                },
              ),
          ]);
          if (!wide) return list;
          return Row(children: [
            SizedBox(width: 340, child: list),
            const VerticalDivider(width: 1),
            Expanded(
              child: AnimatedBuilder(animation: _changes, builder: (c, _) => ListView(padding: const EdgeInsets.all(20), children: _section(c, _sel))),
            ),
          ]);
        },
      );
    });
  }

  /// Одна строка о состоянии раздела под его названием.
  String _summary(_Sec sec, AppModel m) {
    switch (sec) {
      case _Sec.connection:
        final ac = switch (m.vpn?.autoconnect ?? '') { 'always' => 'автоподключение всегда', 'off' => 'без автоподключения', 'last' => 'автоподключение как было', _ => '' };
        final ks = Platform.isAndroid ? 'киллсвитч системный' : KillswitchText.current().supported ? (m.killswitchOn ? 'киллсвитч включён' : 'киллсвитч выключен') : '';
        return [ac, ks, m.degradeAction == 'auto' ? 'узел меняется сам' : 'предлагает сменить узел'].where((e) => e.isNotEmpty).join(' · ');
      case _Sec.routing:
        return Platform.isAndroid ? 'Правила, VPN для приложений, списки блокировок' : 'Правила, профили, списки блокировок';
      case _Sec.nodes:
        final on = m.sources.where((s) => s.enabled).length;
        return 'Свои серверы, источники подписок${m.sources.isEmpty ? '' : ' ($on из ${m.sources.length})'}, WARP';
      case _Sec.notify:
        return switch (m.notifyLevel) { 'all' => 'Показывать все', 'off' => 'Выключены', _ => 'Только важные' };
      case _Sec.look:
        return 'Тема, цвет, список узлов';
      case _Sec.system:
        return 'Автозапуск, трей, выход';
      case _Sec.data:
        return Platform.isAndroid ? 'Резервная копия, журнал, что видят сайты' : 'Резервная копия, что видят сайты';
      case _Sec.about:
        final u = m.status?.update;
        return u != null ? 'Доступна версия ${u.app}' : 'Версия ${(m.status?.version ?? '').split('-').first}, обновление';
    }
  }

  List<Widget> _section(BuildContext context, _Sec sec) {
    final m = widget.model;
    final cs = Theme.of(context).colorScheme;
    final h = Theme.of(context).textTheme.titleSmall?.copyWith(color: cs.primary);
    final small = TextStyle(fontSize: 12, color: cs.onSurfaceVariant);
    Widget title(String t) => Padding(padding: const EdgeInsets.only(top: 8, bottom: 4), child: Text(t, style: h));
    Widget nav(IconData icon, String t, String sub, VoidCallback onTap) => ListTile(contentPadding: EdgeInsets.zero, leading: Icon(icon), title: Text(t), subtitle: Text(sub), trailing: const Icon(Icons.chevron_right), onTap: onTap);
    void push(Widget page) => Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => page));
    switch (sec) {
      case _Sec.connection:
        final ks = KillswitchText.current();
        return [
          // автоподключением управляет демон; на Android VPN при запуске поднимает система («Постоянный VPN») — поля нет
          if ((m.vpn?.autoconnect ?? '').isNotEmpty) ...[
            title('Автоподключение VPN'),
            Text('После перезагрузки, сбоя или перезапуска службы', style: small),
            const SizedBox(height: 8),
            SegmentedButton<String>(
              showSelectedIcon: false,
              segments: const [
                ButtonSegment(value: 'last', label: _SegLabel('Как было'), icon: Icon(Icons.history)),
                ButtonSegment(value: 'always', label: _SegLabel('Всегда'), icon: Icon(Icons.vpn_key)),
                ButtonSegment(value: 'off', label: _SegLabel('Не включать'), icon: Icon(Icons.block)),
              ],
              selected: {m.vpn!.autoconnect},
              onSelectionChanged: (s) => m.setAutoconnect(s.first),
            ),
            const SizedBox(height: 16),
          ],
          title('Если выход перестал отвечать'),
          const SizedBox(height: 4),
          SegmentedButton<String>(
            showSelectedIcon: false,
            segments: const [
              ButtonSegment(value: 'notify', label: _SegLabel('Предложить'), icon: Icon(Icons.notifications_active_outlined)),
              ButtonSegment(value: 'auto', label: _SegLabel('Переключать сам'), icon: Icon(Icons.autorenew)),
            ],
            selected: {m.degradeAction},
            onSelectionChanged: (s) => m.setSetting('degrade.action', s.first),
          ),
          const SizedBox(height: 16),
          title('Защита от утечек'),
          // на Android киллсвитч — системный («Постоянный VPN» и блокировка без VPN): один пункт ведёт в настройки системы
          if (Platform.isAndroid)
            ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.vpn_lock), title: const Text('Киллсвитч'), subtitle: Text(ks.summary), trailing: const Icon(Icons.chevron_right), onTap: AndroidCore.openVpnSettings)
          else
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Киллсвитч'),
              subtitle: Text(ks.summary),
              value: ks.supported && m.killswitchOn,
              onChanged: ks.supported ? (v) => m.setSetting('killswitch.enabled', v ? '1' : '0') : null,
            ),
          if (ks.supported && ks.howToRelease != null) Padding(padding: const EdgeInsets.only(bottom: 4), child: Text(ks.howToRelease!, style: small)),
          if (Platform.isAndroid) ...[
            const SizedBox(height: 8),
            title('Работа в фоне'),
            const _BatteryTile(),
          ],
        ];
      case _Sec.routing:
        return [
          nav(Icons.alt_route, 'Правила маршрутизации', 'Что идёт через VPN, что напрямую, что блокировать; профили', () => push(RulesPage(model: m))),
          if (Platform.isAndroid) nav(Icons.apps, 'VPN для приложений', _appsSummary(m.settings), () => push(AppsPage(model: m))),
          nav(Icons.list_alt, 'Свои списки', m.userSets.isEmpty ? 'Rule-set по адресу: появляются в условиях правил' : 'Добавлено: ${m.userSets.length}', () => push(UserSetsPage(model: m))),
          const SizedBox(height: 8),
          // на Android не проверялось (нет способа проверить DNS из adb): переключатель только на компьютере
          if (!Platform.isAndroid)
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Быстрый DNS (FakeIP), эксперимент'),
              subtitle: const Text(
                  'Только в режиме «Всё через выход»; нужен перезапуск VPN. Сайты с частным адресом добавьте в правила «напрямую».'),
              value: m.fakeipOn,
              onChanged: (v) => m.setSetting('dns.fakeip', v ? '1' : '0'),
            ),
          const SizedBox(height: 8),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Раздавать прокси в локальную сеть'),
            subtitle: const Text('Другие устройства сети (телевизор, консоль, второй компьютер) подключаются к Brod как к прокси и идут через ваш выход. Защита: логин и пароль'),
            value: m.proxyLanOn,
            onChanged: (v) async {
              await m.setSetting('proxy.lan', v ? '1' : '0');
              await m.loadProxyShare();
            },
          ),
          if (m.proxyLanOn) _ProxyShareCard(model: m),
          const SizedBox(height: 8),
          title('Списки блокировок'),
          ListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Заблокированное в РФ, банки, «только внутри РФ»'),
            subtitle: const Text('Обновляются сами раз в 12 часов, пока VPN включён'),
            trailing: OutlinedButton(onPressed: m.updateRuleSets, child: const Text('Обновить сейчас')),
          ),
        ];
      case _Sec.nodes:
        return [
          nav(Icons.storage_outlined, 'Мои серверы', 'Свой сервер по ссылке; всегда наверху списка', () => push(OwnPage(model: m))),
          nav(Icons.rss_feed, 'Источники подписок', m.sources.isEmpty ? 'Загрузка…' : '${m.sources.where((s) => s.enabled).length} из ${m.sources.length} включено', () => push(SourcesPage(model: m))),
          const SizedBox(height: 8),
          nav(Icons.cloud_outlined, 'Cloudflare WARP', 'Подбор под сеть, ручные параметры, аккаунт', () => push(WarpPage(model: m))),
          title('Проверка узлов через WARP'),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Проверять узлы через WARP'),
            subtitle: const Text('Цепочка WARP → узел: часть узлов отвечает только так'),
            value: m.viaWarp,
            onChanged: m.status?.warpEnabled == true ? m.setViaWarp : null,
          ),
          ListTile(
            contentPadding: EdgeInsets.zero,
            leading: m.warpBusy ? const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.cloud_outlined),
            title: Text(m.status?.warpEnabled == true ? 'Аккаунт WARP: настроен' : 'Аккаунт WARP: не настроен'),
            subtitle: Text(m.warpBusy ? 'Регистрирую… (до двух минут)' : 'Если сеть режет, регистрация пойдёт через выбранный узел'),
            enabled: !m.warpBusy,
            onTap: () => warpRegisterDialog(context, m),
          ),
          ListTile(
            contentPadding: EdgeInsets.zero,
            leading: const Icon(Icons.file_open_outlined),
            title: const Text('Загрузить аккаунт WARP из файла'),
            subtitle: const Text('warp-account.json с другого устройства'),
            enabled: !m.warpBusy,
            onTap: () => warpImportDialog(context, m),
          ),
        ];
      case _Sec.notify:
        return [
          title('Какие уведомления показывать'),
          const SizedBox(height: 4),
          SegmentedButton<String>(
            showSelectedIcon: false,
            segments: const [
              ButtonSegment(value: 'important', label: _SegLabel('Важные'), icon: Icon(Icons.label_important_outline)),
              ButtonSegment(value: 'all', label: _SegLabel('Все'), icon: Icon(Icons.notifications_outlined)),
              ButtonSegment(value: 'off', label: _SegLabel('Выкл'), icon: Icon(Icons.notifications_off_outlined)),
            ],
            selected: {m.notifyLevel},
            onSelectionChanged: (s) => m.setSetting('notify.level', s.first),
          ),
          Padding(padding: const EdgeInsets.only(top: 6), child: Text('«Важные» — только значимое об активном узле', style: small)),
        ];
      case _Sec.look:
        final p = widget.prefs;
        if (p == null) return [const Text('Недоступно')];
        return [
          title('Тема'),
          SegmentedButton<String>(
            showSelectedIcon: false,
            // узкий экран и крупный шрифт: меньше отступов, подпись сжимается по ширине сегмента (_SegLabel), а не обрезается
            style: SegmentedButton.styleFrom(padding: const EdgeInsets.symmetric(horizontal: 6), visualDensity: VisualDensity.compact),
            segments: const [
              ButtonSegment(value: 'system', label: _SegLabel('Системная'), icon: Icon(Icons.brightness_auto)),
              ButtonSegment(value: 'light', label: _SegLabel('Светлая'), icon: Icon(Icons.light_mode_outlined)),
              ButtonSegment(value: 'dark', label: _SegLabel('Тёмная'), icon: Icon(Icons.dark_mode_outlined)),
            ],
            selected: {p.themeMode},
            onSelectionChanged: (s) => p.update(themeMode: s.first),
          ),
          const SizedBox(height: 12),
          Wrap(spacing: 10, runSpacing: 8, children: [
            for (var i = 0; i < accentPalette.length; i++)
              Tooltip(
                message: accentPalette[i].$1,
                child: InkWell(
                  customBorder: const CircleBorder(),
                  onTap: () => p.update(accent: i),
                  child: Container(
                    width: 34,
                    height: 34,
                    decoration: BoxDecoration(shape: BoxShape.circle, color: Color(accentPalette[i].$2), border: Border.all(color: p.accent == i ? cs.onSurface : Colors.transparent, width: 3)),
                    child: p.accent == i ? const Icon(Icons.check, size: 18, color: Colors.white) : null,
                  ),
                ),
              ),
          ]),
          SwitchListTile(contentPadding: EdgeInsets.zero, title: const Text('Чисто чёрный фон (AMOLED)'), subtitle: const Text('Только в тёмной теме'), value: p.amoled, onChanged: (v) => p.update(amoled: v)),
          const Divider(height: 32),
          title('Список узлов'),
          SwitchListTile(contentPadding: EdgeInsets.zero, title: const Text('Плотный список узлов'), value: p.compact, onChanged: (v) => p.update(compact: v)),
          SwitchListTile(contentPadding: EdgeInsets.zero, title: const Text('Группировать узлы по странам'), value: p.groupByCountry, onChanged: (v) => p.update(groupByCountry: v)),
          if (Platform.isAndroid) ...[
            const Divider(height: 32),
            title('Добавление серверов'),
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Предлагать добавить ссылку из буфера обмена'),
              subtitle: const Text('При возврате в приложение: если скопирована ссылка на сервер (vless://, vmess://, trojan:// и т. п.). Android в этот момент показывает, что приложение читает буфер'),
              value: p.clipboardImport,
              onChanged: (v) => p.update(clipboardImport: v),
            ),
          ],
        ];
      case _Sec.system:
        final p = widget.prefs;
        return [
          if (Autostart.supported)
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Запускать при входе в систему'),
              subtitle: Text(_autostartError ?? 'Значок в трее и уведомления сразу после входа'),
              value: _autostart ?? false,
              onChanged: _autostart == null ? null : _setAutostart,
            ),
          // одна настройка для любого запуска — и ручного, и при входе в систему
          if (!Platform.isAndroid && p != null) ...[
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Запускать свёрнутым в трей'),
              subtitle: const Text('Не открывать окно при запуске и входе в систему'),
              value: p.startHidden,
              onChanged: (v) => p.update(startHidden: v),
            ),
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Крестик сворачивает в трей'),
              subtitle: const Text('VPN и демон продолжают работать; выход — в меню трея'),
              value: p.closeToTray,
              onChanged: (v) => p.update(closeToTray: v),
            ),
            if (widget.onQuit != null) Align(alignment: Alignment.centerLeft, child: TextButton.icon(onPressed: widget.onQuit, icon: const Icon(Icons.power_settings_new), label: const Text('Выйти из приложения'))),
          ],
        ];
      case _Sec.data:
        return [
          title('Резервная копия'),
          ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.save_alt), title: const Text('Сохранить копию'), subtitle: const Text('Правила, настройки, названия и свои серверы в одном файле'), onTap: () => backupDialog(context, m, restore: false)),
          ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.restore), title: const Text('Восстановить из копии'), onTap: () => backupDialog(context, m, restore: true)),
          const Divider(height: 32),
          title('Диагностика'),
          nav(Icons.fact_check_outlined, 'Диагностический отчёт', 'Тесты DNS, связи, WARP и узлов одной кнопкой; файл для разбора неполадок', () => push(ReportPage(model: m))),
          nav(Icons.travel_explore, 'Что видят сайты', 'Адрес, страна по нескольким базам, тип адреса, часовой пояс, IPv6, DNS', () => push(DiagnosticsPage(model: m))),
          if (Platform.isAndroid) ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.share), title: const Text('Поделиться журналом'), subtitle: const Text('Файл для разбора неполадок'), onTap: AndroidCore.shareLogs),
        ];
      case _Sec.about:
        final u = m.status?.update;
        return [
          title('Версия'),
          ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.verified_outlined), title: Text('Brod ${(m.status?.version ?? '').split('-').first}'), subtitle: Text(m.status?.version ?? '')),
          const SizedBox(height: 8),
          title('Обновление приложения'),
          nav(Icons.system_update_alt, 'Проверка и установка', u != null ? 'Доступна версия ${u.app}${u.download == 'ready' ? ' · скачана и проверена' : ''}' : 'Проверка, скачивание, установка', () => showUpdateDialog(context, m)),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Проверять обновления раз в сутки'),
            subtitle: const Text('Только уведомление; скачивание и установка — по кнопке'),
            value: m.settings['update.check'] != '0',
            onChanged: (v) => m.setSetting('update.check', v ? '1' : '0'),
          ),
        ];
    }
  }
}

/// Экран одного раздела настроек (узкий экран).
class _SectionScreen extends StatelessWidget {
  const _SectionScreen({required this.title, required this.changes, required this.builder});

  final String title;
  final Listenable changes;
  final List<Widget> Function(BuildContext) builder;

  @override
  Widget build(BuildContext context) => Scaffold(
        appBar: AppBar(title: Text(title)),
        body: AnimatedBuilder(animation: changes, builder: (c, _) => ListView(padding: const EdgeInsets.all(16), children: builder(c))),
      );
}

/// Краткое описание режима per-app для строки настроек.
String _appsSummary(Map<String, String> s) {
  final mode = s['tun.apps_mode'] ?? '';
  final n = (s['tun.apps'] ?? '').split(',').where((p) => p.trim().isNotEmpty).length;
  return switch (mode) {
    'include' => 'Только выбранные: $n',
    'exclude' => 'Все, кроме выбранных: $n',
    _ => 'Для всех приложений',
  };
}

/// Исключение из оптимизации батареи: без него система может остановить службу VPN в фоне.
class _BatteryTile extends StatefulWidget {
  const _BatteryTile();

  @override
  State<_BatteryTile> createState() => _BatteryTileState();
}

class _BatteryTileState extends State<_BatteryTile> {
  bool? _ignored;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final v = await AndroidCore.batteryIgnored();
    if (mounted) setState(() => _ignored = v);
  }

  @override
  Widget build(BuildContext context) {
    final ok = _ignored == true;
    return ListTile(
      contentPadding: EdgeInsets.zero,
      leading: Icon(ok ? Icons.battery_charging_full : Icons.battery_alert),
      title: const Text('Работа в фоне'),
      subtitle: Text(ok
          ? 'Разрешена'
          : 'Система может остановить VPN. На HyperOS включите ещё «Автозапуск»'),
      onTap: ok
          ? null
          : () async {
              final v = await AndroidCore.requestIgnoreBattery();
              if (mounted) setState(() => _ignored = v);
            },
    );
  }
}

/// Подпись сегмента с иконкой: в одну строку, при нехватке ширины (узкий экран, крупный шрифт системы) уменьшается, а не обрезается.
class _SegLabel extends StatelessWidget {
  const _SegLabel(this.text);

  final String text;

  @override
  Widget build(BuildContext context) => FittedBox(fit: BoxFit.scaleDown, child: Text(text, maxLines: 1, softWrap: false));
}

/// Данные для подключения к раздаваемому прокси: адреса, логин и пароль, с копированием. Пароль показывается только по нажатию.
class _ProxyShareCard extends StatefulWidget {
  const _ProxyShareCard({required this.model});

  final AppModel model;

  @override
  State<_ProxyShareCard> createState() => _ProxyShareCardState();
}

class _ProxyShareCardState extends State<_ProxyShareCard> {
  bool _showPass = false;

  @override
  void initState() {
    super.initState();
    widget.model.loadProxyShare();
  }

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return ListenableBuilder(
      listenable: widget.model,
      builder: (context, _) {
        final s = widget.model.proxyShare;
        if (s == null || !s.lan) {
          return const Padding(padding: EdgeInsets.symmetric(vertical: 8), child: Text('Раздача включается: ядро перезапускается, данные появятся через несколько секунд.'));
        }
        Widget row(String k, String v, {bool secret = false}) => ListTile(
              dense: true,
              contentPadding: EdgeInsets.zero,
              title: Text(secret && !_showPass ? '••••••••••••' : v),
              subtitle: Text(k),
              trailing: Row(mainAxisSize: MainAxisSize.min, children: [
                if (secret) IconButton(icon: Icon(_showPass ? Icons.visibility_off : Icons.visibility), tooltip: 'Показать', onPressed: () => setState(() => _showPass = !_showPass)),
                IconButton(
                  icon: const Icon(Icons.copy),
                  tooltip: 'Копировать',
                  onPressed: () async {
                    await Clipboard.setData(ClipboardData(text: v));
                    if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$k скопирован')));
                  },
                ),
              ]),
            );
        return Card.filled(
          child: Padding(
            padding: const EdgeInsets.all(12),
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text('SOCKS5 и HTTP на одном порту. Введите на другом устройстве:', style: Theme.of(context).textTheme.bodySmall),
              if (s.endpoints.isEmpty) const Padding(padding: EdgeInsets.symmetric(vertical: 6), child: Text('Адресов в локальной сети не найдено: проверьте, что устройство в сети.')),
              for (final e in s.endpoints) row('Адрес и порт', e),
              row('Логин', s.username),
              row('Пароль', s.password, secret: true),
              const SizedBox(height: 4),
              Text(
                'Внимание: пароль в локальной сети передаётся без шифрования, а подключившийся пользуется вашим выходом. Включайте только в сети, где доверяете устройствам; в публичных сетях держите выключенным.',
                style: Theme.of(context).textTheme.bodySmall?.copyWith(color: cs.error),
              ),
            ]),
          ),
        );
      },
    );
  }
}
