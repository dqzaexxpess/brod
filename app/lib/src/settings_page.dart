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
import 'node_dialogs.dart';
import 'settings_search.dart';
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
  const SettingsPage({super.key, required this.model, this.visible = true, this.prefs, this.onQuit, this.onShowNodes});

  final AppModel model;
  final bool visible;
  final Prefs? prefs;
  final VoidCallback? onQuit;
  final VoidCallback? onShowNodes; // перейти на вкладку «Узлы» (для пунктов поиска про действия из меню узла)

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  bool? _autostart; // null — ещё не прочитан
  String? _autostartError;
  final _tick = ValueNotifier<int>(0); // перерисовка открытого раздела при изменении локального состояния
  _Sec _sel = _Sec.connection;
  final _query = TextEditingController();
  final _searchFocus = FocusNode();
  final _anchors = <String, GlobalKey>{}; // якоря внутри разделов: к ним прокручивается экран из результатов поиска
  String? _hl; // подсвеченный якорь
  GlobalKey _anchor(String id) => _anchors.putIfAbsent(id, GlobalKey.new);

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
    _query.dispose();
    _searchFocus.dispose();
    _tick.dispose();
    super.dispose();
  }


  /// Всё, что можно найти поиском: настройки и функции со словами, по которым их ищут. Пункт с page сразу открывает страницу или окно; остальные
  /// открывают раздел, прокручивают к настройке и подсвечивают её (id — якорь внутри раздела). Платформенные пункты — только там, где они есть.
  List<SearchEntry> get _index {
    final android = Platform.isAndroid;
    return [
      const SearchEntry('Автоподключение VPN', 'connection', 'запуск после перезагрузки включать сам постоянный vpn always-on автозапуск vpn', id: 'autoconnect'),
      const SearchEntry('Если выход перестал отвечать', 'connection', 'автозамена узла переключать сам предложить сменить деградация сбой узел умер автопереключение', id: 'degrade'),
      const SearchEntry('Киллсвитч', 'connection', 'защита от утечек блокировка интернета без vpn kill switch утечка обрыв', id: 'killswitch'),
      if (android) const SearchEntry('Работа в фоне', 'connection', 'батарея экономия энергии оптимизация не убивать приложение фон не выгружать', id: 'battery'),
      const SearchEntry('Правила маршрутизации', 'routing', 'что через vpn напрямую блокировать профили домены маршруты split правила', id: 'rules', page: true),
      if (android) const SearchEntry('VPN для приложений', 'routing', 'приложения исключить только выбранные раздельное туннелирование список приложений', id: 'apps', page: true),
      const SearchEntry('Свои списки', 'routing', 'rule-set наборы правил подписка на список адресов', id: 'usersets', page: true),
      if (!android) const SearchEntry('Быстрый DNS (FakeIP)', 'routing', 'dns fakeip днс ускорение эксперимент', id: 'fakeip'),
      const SearchEntry('Раздавать прокси в локальную сеть', 'routing', 'lan телевизор консоль второй компьютер socks http прокси для других устройств раздача логин пароль', id: 'lanproxy'),
      const SearchEntry('Списки блокировок', 'routing', 'заблокированное в рф банки только внутри рф обновить rule-set', id: 'blocklists'),
      // действия из меню узла (⋮ у узла в списке): поиск ведёт на вкладку «Узлы» и подсказывает, где они
      const SearchEntry('QR-код сервера', 'nodes', 'показать qr код поделиться отсканировать камерой передать сервер', id: 'node:Узел → ⋮ → «Показать QR-код»', page: true),
      const SearchEntry('Поделиться ссылкой на сервер', 'nodes', 'поделиться ссылка отправить сервер vless', id: 'node:Узел → ⋮ → «Поделиться ссылкой»', page: true),
      const SearchEntry('Проверить пинг узла', 'nodes', 'пинг задержка проверить задержку', id: 'node:Узел → ⋮ → «Проверить пинг»', page: true),
      const SearchEntry('Замерить скорость узла', 'nodes', 'скорость замер speedtest мбит', id: 'node:Узел → ⋮ → «Замерить скорость»', page: true),
      const SearchEntry('Проверить узел для звонков', 'nodes', 'звонки голос видео udp telegram whatsapp discord проверка для звонков', id: 'node:Узел → ⋮ → «Проверить для звонков»', page: true),
      const SearchEntry('Проверить страну и чистоту IP', 'nodes', 'страна ip чистый грязный репутация проверить выход', id: 'node:Узел → ⋮ → «Проверить страну и чистоту IP»', page: true),
      const SearchEntry('Проверить звенья цепочки', 'nodes', 'цепочка звено умерло не отвечает проверить звенья', id: 'node:Цепочка → ⋮ → «Проверить звенья»', page: true),
      const SearchEntry('Переименовать узел', 'nodes', 'название имя переименовать узел', id: 'node:Узел → ⋮ → «Переименовать»', page: true),
      const SearchEntry('Мои серверы', 'nodes', 'свой сервер ссылка vless vmess trojan shadowsocks wireguard добавить вставить', id: 'own', page: true),
      const SearchEntry('Собрать цепочку прокси', 'nodes', 'цепочка хоп hop несколько узлов подряд двойной прокси каскад', id: 'chain', page: true),
      const SearchEntry('Источники подписок', 'nodes', 'подписка subscription url список узлов квота трафик срок обновить', id: 'sources', page: true),
      const SearchEntry('Cloudflare WARP', 'nodes', 'warp варп аккаунт wireguard подбор endpoint', id: 'warp', page: true),
      const SearchEntry('Проверять узлы через WARP', 'nodes', 'warp проверка узлов цепочка', id: 'viawarp'),
      const SearchEntry('Аккаунт WARP', 'nodes', 'warp зарегистрировать создать аккаунт регистрация', id: 'warpacct'),
      const SearchEntry('Загрузить аккаунт WARP из файла', 'nodes', 'warp-account.json импорт аккаунта файл', id: 'warpimport'),
      const SearchEntry('Какие уведомления показывать', 'notify', 'уведомления оповещения важные все выключить тишина', id: 'notify'),
      const SearchEntry('Тема', 'look', 'тёмная светлая системная цвет акцент оформление ночная свой цвет hex палитра', id: 'theme'),
      const SearchEntry('Размер текста', 'look', 'шрифт крупнее мельче масштаб текста читаемость', id: 'textscale'),
      const SearchEntry('Чисто чёрный фон (AMOLED)', 'look', 'amoled чёрная тема экран', id: 'amoled'),
      const SearchEntry('Плотный список узлов', 'look', 'компактный список узлов плотность', id: 'compact'),
      const SearchEntry('Группировать узлы по странам', 'look', 'страны группировка список узлов', id: 'group'),
      if (android) const SearchEntry('Предлагать добавить ссылку из буфера обмена', 'look', 'буфер обмена clipboard ссылка vless вставка импорт', id: 'clipboard'),
      if (_hasSystem && Autostart.supported) const SearchEntry('Запускать при входе в систему', 'system', 'автозапуск автозагрузка старт с системой', id: 'autostart'),
      if (_hasSystem && !android) const SearchEntry('Запускать свёрнутым в трей', 'system', 'трей скрытый запуск окно не показывать', id: 'hidden'),
      if (_hasSystem && !android) const SearchEntry('Крестик сворачивает в трей', 'system', 'закрытие окна выход трей фон', id: 'closetray'),
      const SearchEntry('Сохранить копию', 'data', 'резервная копия бэкап экспорт настроек правил серверов сохранить в файл', id: 'backupsave', page: true),
      const SearchEntry('Восстановить из копии', 'data', 'резервная копия бэкап импорт восстановить из файла', id: 'backuprestore', page: true),
      const SearchEntry('Диагностический отчёт', 'data', 'отчёт диагностика тесты dns связи warp логи неполадки проблема не работает', id: 'report', page: true),
      const SearchEntry('Что видят сайты', 'data', 'мой ip адрес страна утечка dns webrtc анонимность проверка', id: 'sites', page: true),
      if (android) const SearchEntry('Поделиться журналом', 'data', 'лог журнал файл для разбора неполадок', id: 'sharelogs', page: true),
      const SearchEntry('Версия и обновление приложения', 'about', 'обновить скачать новая версия проверка обновлений ota установить', id: 'update', page: true),
      const SearchEntry('Проверять обновления раз в сутки', 'about', 'автообновление уведомление о новой версии', id: 'autocheck'),
    ];
  }

  /// Свой акцентный цвет: HEX вида #3F7DE0.
  Future<void> _customAccentDialog(BuildContext context, Prefs p) async {
    final c = TextEditingController(text: p.accent < 0 ? '#${(p.accentColor.toARGB32() & 0xFFFFFF).toRadixString(16).padLeft(6, '0').toUpperCase()}' : '');
    final color = await showDialog<Color>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setS) {
          final parsed = parseHexColor(c.text);
          return AlertDialog(
            title: const Text('Свой цвет'),
            content: Column(mainAxisSize: MainAxisSize.min, children: [
              TextField(
                controller: c,
                autofocus: true,
                maxLength: 7,
                decoration: InputDecoration(labelText: 'HEX, например #3F7DE0', errorText: c.text.isNotEmpty && parsed == null ? 'Нужно шесть цифр или букв a–f' : null, counterText: ''),
                onChanged: (_) => setS(() {}),
                onSubmitted: (_) => parsed == null ? null : Navigator.pop(ctx, parsed),
              ),
              const SizedBox(height: 12),
              Container(width: 48, height: 48, decoration: BoxDecoration(shape: BoxShape.circle, color: parsed ?? Colors.transparent, border: Border.all(color: Theme.of(ctx).colorScheme.outline))),
            ]),
            actions: [
              TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Отмена')),
              FilledButton(onPressed: parsed == null ? null : () => Navigator.pop(ctx, parsed), child: const Text('Применить')),
            ],
          );
        },
      ),
    );
    c.dispose();
    if (color != null) p.update(accent: -1, customAccent: color.toARGB32());
  }

  /// Страница или окно, которое открывает пункт поиска с page.
  void _openPage(BuildContext context, String id) {
    final m = widget.model;
    if (id.startsWith('node:')) {
      // эти действия живут в меню узла: переходим на вкладку «Узлы» и говорим, куда нажать
      widget.onShowNodes?.call();
      m.showHint(id.substring(5));
      return;
    }
    void push(Widget page) => Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => page));
    switch (id) {
      case 'rules':
        push(RulesPage(model: m));
      case 'apps':
        push(AppsPage(model: m));
      case 'usersets':
        push(UserSetsPage(model: m));
      case 'own':
        push(OwnPage(model: m));
      case 'chain':
        chainDialog(context, m);
      case 'sources':
        push(SourcesPage(model: m));
      case 'warp':
        push(WarpPage(model: m));
      case 'backupsave':
        backupDialog(context, m, restore: false);
      case 'backuprestore':
        backupDialog(context, m, restore: true);
      case 'report':
        push(ReportPage(model: m));
      case 'sites':
        push(DiagnosticsPage(model: m));
      case 'sharelogs':
        AndroidCore.shareLogs();
      case 'update':
        showUpdateDialog(context, m);
    }
  }

  /// Открывает найденное: страницу сразу, настройку — через её раздел с прокруткой и подсветкой.
  void _openEntry(BuildContext context, SearchEntry e, bool wide) {
    _searchFocus.unfocus();
    if (e.page) {
      _openPage(context, e.id);
      return;
    }
    final sec = _secById(e.section);
    if (sec == null) return;
    _openSection(context, sec, wide);
    if (e.id.isNotEmpty) _reveal(e.id, 0);
  }

  /// Ждёт, пока раздел построится (на телефоне — пока откроется экран), прокручивает к якорю и подсвечивает его на пару секунд.
  void _reveal(String id, int tries) {
    Future.delayed(Duration(milliseconds: tries == 0 ? 350 : 100), () {
      if (!mounted) return;
      final ctx = _anchors[id]?.currentContext;
      if (ctx == null || !ctx.mounted) {
        if (tries < 15) _reveal(id, tries + 1);
        return;
      }
      _hl = id;
      _tick.value++;
      Scrollable.ensureVisible(ctx, alignment: 0.2, duration: const Duration(milliseconds: 300), curve: Curves.easeOut);
      Future.delayed(const Duration(milliseconds: 2500), () {
        if (mounted && _hl == id) {
          _hl = null;
          _tick.value++;
        }
      });
    });
  }

  _Sec? _secById(String id) {
    for (final s in _Sec.values) {
      if (s.name == id) return s;
    }
    return null;
  }

  void _openSection(BuildContext context, _Sec sec, bool wide) {
    _searchFocus.unfocus(); // иначе при возврате с экрана раздела поле поиска снова получает фокус и поднимает клавиатуру
    if (wide) {
      setState(() {
        _sel = sec;
        _query.clear();
      });
    } else {
      Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => _SectionScreen(title: _secInfo[sec]!.title, changes: _changes, builder: (c) => _section(c, sec))));
    }
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
            Padding(
              padding: const EdgeInsets.fromLTRB(8, 4, 8, 8),
              child: SearchBar(
                controller: _query,
                focusNode: _searchFocus,
                onTapOutside: (_) => _searchFocus.unfocus(),
                hintText: 'Найти настройку или функцию',
                leading: const Icon(Icons.search),
                elevation: const WidgetStatePropertyAll(0),
                trailing: [if (_query.text.isNotEmpty) IconButton(icon: const Icon(Icons.close), tooltip: 'Очистить', onPressed: () => setState(_query.clear))],
                onChanged: (_) => setState(() {}),
              ),
            ),
            if (_query.text.trim().isNotEmpty) ...[
              for (final e in searchSettings(_query.text, _index))
                if (_secById(e.section) case final sec? when secs.contains(sec))
                  ListTile(
                    leading: Icon(_secInfo[sec]!.icon),
                    title: Text(e.title),
                    subtitle: Text(_secInfo[sec]!.title),
                    trailing: wide ? null : const Icon(Icons.chevron_right),
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                    onTap: () => _openEntry(context, e, wide),
                  ),
              if (searchSettings(_query.text, _index).isEmpty) const Padding(padding: EdgeInsets.all(16), child: Text('Ничего не найдено. Попробуйте другое слово: «прокси», «киллсвитч», «обновление».')),
            ] else
              for (final sec in secs)
                ListTile(
                  leading: Icon(_secInfo[sec]!.icon),
                  title: Text(_secInfo[sec]!.title),
                  subtitle: Text(_summary(sec, m), maxLines: 2, overflow: TextOverflow.ellipsis),
                  trailing: wide ? null : const Icon(Icons.chevron_right),
                  selected: wide && sec == _sel,
                  selectedTileColor: Theme.of(context).colorScheme.secondaryContainer.withValues(alpha: 0.5),
                  shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                  onTap: () => _openSection(context, sec, wide),
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
    Widget hl(String id, Widget w) => _Anchor(key: _anchor(id), lit: _hl == id, child: w);
    Widget title(String t) => Padding(padding: const EdgeInsets.only(top: 8, bottom: 4), child: Text(t, style: h));
    Widget nav(IconData icon, String t, String sub, VoidCallback onTap) => ListTile(contentPadding: EdgeInsets.zero, leading: Icon(icon), title: Text(t), subtitle: Text(sub), trailing: const Icon(Icons.chevron_right), onTap: onTap);
    void push(Widget page) => Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => page));
    switch (sec) {
      case _Sec.connection:
        final ks = KillswitchText.current();
        return [
          // автоподключением управляет демон; на Android VPN при запуске поднимает система («Постоянный VPN») — поля нет
          if ((m.vpn?.autoconnect ?? '').isNotEmpty) ...[
            hl('autoconnect', title('Автоподключение VPN')),
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
          hl('degrade', title('Если выход перестал отвечать')),
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
          hl('killswitch', title('Защита от утечек')),
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
            hl('battery', title('Работа в фоне')),
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
            hl('fakeip', SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Быстрый DNS (FakeIP), эксперимент'),
              subtitle: const Text(
                  'Только в режиме «Всё через выход»; нужен перезапуск VPN. Сайты с частным адресом добавьте в правила «напрямую».'),
              value: m.fakeipOn,
              onChanged: (v) => m.setSetting('dns.fakeip', v ? '1' : '0'),
            )),
          const SizedBox(height: 8),
          hl('lanproxy', SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Раздавать прокси в локальную сеть'),
            subtitle: const Text('Другие устройства сети (телевизор, консоль, второй компьютер) подключаются к Brod как к прокси и идут через ваш выход. Защита: логин и пароль'),
            value: m.proxyLanOn,
            onChanged: (v) async {
              await m.setSetting('proxy.lan', v ? '1' : '0');
              await m.loadProxyShare();
            },
          )),
          if (m.proxyLanOn) _ProxyShareCard(model: m),
          const SizedBox(height: 8),
          hl('blocklists', title('Списки блокировок')),
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
          hl('viawarp', title('Проверка узлов через WARP')),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Проверять узлы через WARP'),
            subtitle: const Text('Цепочка WARP → узел: часть узлов отвечает только так'),
            value: m.viaWarp,
            onChanged: m.status?.warpEnabled == true ? m.setViaWarp : null,
          ),
          hl('warpacct', ListTile(
            contentPadding: EdgeInsets.zero,
            leading: m.warpBusy ? const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.cloud_outlined),
            title: Text(m.status?.warpEnabled == true ? 'Аккаунт WARP: настроен' : 'Аккаунт WARP: не настроен'),
            subtitle: Text(m.warpBusy ? 'Регистрирую… (до двух минут)' : 'Если сеть режет, регистрация пойдёт через выбранный узел'),
            enabled: !m.warpBusy,
            onTap: () => warpRegisterDialog(context, m),
          )),
          hl('warpimport', ListTile(
            contentPadding: EdgeInsets.zero,
            leading: const Icon(Icons.file_open_outlined),
            title: const Text('Загрузить аккаунт WARP из файла'),
            subtitle: const Text('warp-account.json с другого устройства'),
            enabled: !m.warpBusy,
            onTap: () => warpImportDialog(context, m),
          )),
        ];
      case _Sec.notify:
        return [
          hl('notify', title('Какие уведомления показывать')),
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
          hl('theme', title('Тема')),
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
            Tooltip(
              message: 'Свой цвет',
              child: InkWell(
                customBorder: const CircleBorder(),
                onTap: () => _customAccentDialog(context, p),
                child: Container(
                  width: 34,
                  height: 34,
                  decoration: BoxDecoration(shape: BoxShape.circle, color: p.accent < 0 ? p.accentColor : null, border: Border.all(color: p.accent < 0 ? cs.onSurface : cs.outline, width: p.accent < 0 ? 3 : 1)),
                  child: Icon(p.accent < 0 ? Icons.check : Icons.palette_outlined, size: 18, color: p.accent < 0 ? Colors.white : cs.onSurfaceVariant),
                ),
              ),
            ),
          ]),
          hl('amoled', SwitchListTile(contentPadding: EdgeInsets.zero, title: const Text('Чисто чёрный фон (AMOLED)'), subtitle: const Text('Только в тёмной теме'), value: p.amoled, onChanged: (v) => p.update(amoled: v))),
          const Divider(height: 32),
          hl('textscale', title('Размер текста')),
          const SizedBox(height: 4),
          SegmentedButton<double>(
            showSelectedIcon: false,
            style: SegmentedButton.styleFrom(padding: const EdgeInsets.symmetric(horizontal: 6), visualDensity: VisualDensity.compact),
            segments: [for (final t in textScales) ButtonSegment(value: t.$2, label: _SegLabel(t.$1))],
            selected: {textScales.map((t) => t.$2).reduce((a, b) => (a - p.textScale).abs() <= (b - p.textScale).abs() ? a : b)},
            onSelectionChanged: (v) => p.update(textScale: v.first),
          ),
          const Divider(height: 32),
          hl('nodelist', title('Список узлов')),
          hl('compact', SwitchListTile(contentPadding: EdgeInsets.zero, title: const Text('Плотный список узлов'), value: p.compact, onChanged: (v) => p.update(compact: v))),
          hl('group', SwitchListTile(contentPadding: EdgeInsets.zero, title: const Text('Группировать узлы по странам'), value: p.groupByCountry, onChanged: (v) => p.update(groupByCountry: v))),
          if (Platform.isAndroid) ...[
            const Divider(height: 32),
            hl('clipboard', title('Добавление серверов')),
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
            hl('autostart', SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Запускать при входе в систему'),
              subtitle: Text(_autostartError ?? 'Значок в трее и уведомления сразу после входа'),
              value: _autostart ?? false,
              onChanged: _autostart == null ? null : _setAutostart,
            )),
          // одна настройка для любого запуска — и ручного, и при входе в систему
          if (!Platform.isAndroid && p != null) ...[
            hl('hidden', SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Запускать свёрнутым в трей'),
              subtitle: const Text('Не открывать окно при запуске и входе в систему'),
              value: p.startHidden,
              onChanged: (v) => p.update(startHidden: v),
            )),
            hl('closetray', SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Крестик сворачивает в трей'),
              subtitle: const Text('VPN и демон продолжают работать; выход — в меню трея'),
              value: p.closeToTray,
              onChanged: (v) => p.update(closeToTray: v),
            )),
            if (widget.onQuit != null) Align(alignment: Alignment.centerLeft, child: TextButton.icon(onPressed: widget.onQuit, icon: const Icon(Icons.power_settings_new), label: const Text('Выйти из приложения'))),
          ],
        ];
      case _Sec.data:
        return [
          hl('backup', title('Резервная копия')),
          hl('backupsave', ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.save_alt), title: const Text('Сохранить копию'), subtitle: const Text('Правила, настройки, названия и свои серверы в одном файле'), onTap: () => backupDialog(context, m, restore: false))),
          hl('backuprestore', ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.restore), title: const Text('Восстановить из копии'), onTap: () => backupDialog(context, m, restore: true))),
          const Divider(height: 32),
          hl('diag', title('Диагностика')),
          nav(Icons.fact_check_outlined, 'Диагностический отчёт', 'Тесты DNS, связи, WARP и узлов одной кнопкой; файл для разбора неполадок', () => push(ReportPage(model: m))),
          nav(Icons.travel_explore, 'Что видят сайты', 'Адрес, страна по нескольким базам, тип адреса, часовой пояс, IPv6, DNS', () => push(DiagnosticsPage(model: m))),
          if (Platform.isAndroid) hl('sharelogs', ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.share), title: const Text('Поделиться журналом'), subtitle: const Text('Файл для разбора неполадок'), onTap: AndroidCore.shareLogs)),
        ];
      case _Sec.about:
        final u = m.status?.update;
        return [
          hl('version', title('Версия')),
          ListTile(contentPadding: EdgeInsets.zero, leading: const Icon(Icons.verified_outlined), title: Text('Brod ${(m.status?.version ?? '').split('-').first}'), subtitle: Text(m.status?.version ?? '')),
          const SizedBox(height: 8),
          title('Обновление приложения'),
          nav(Icons.system_update_alt, 'Проверка и установка', u != null ? 'Доступна версия ${u.app}${u.download == 'ready' ? ' · скачана и проверена' : ''}' : 'Проверка, скачивание, установка', () => showUpdateDialog(context, m)),
          hl('autocheck', SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Проверять обновления раз в сутки'),
            subtitle: const Text('Только уведомление; скачивание и установка — по кнопке'),
            value: m.settings['update.check'] != '0',
            onChanged: (v) => m.setSetting('update.check', v ? '1' : '0'),
          )),
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
/// Якорь внутри раздела: сюда прокручивает результат поиска, и на пару секунд это место подсвечивается.
class _Anchor extends StatelessWidget {
  const _Anchor({super.key, required this.lit, required this.child});
  final bool lit;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    // Material, а не DecoratedBox: ListTile рисует фон и всплески на ближайшем Material, цвет под ним ему мешает; цвет Material плавно меняется сам
    return Material(
      type: MaterialType.canvas,
      color: lit ? cs.primaryContainer.withValues(alpha: 0.7) : Colors.transparent,
      borderRadius: BorderRadius.circular(12),
      animationDuration: const Duration(milliseconds: 300),
      child: child,
    );
  }
}

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
