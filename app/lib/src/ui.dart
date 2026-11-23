import 'package:flutter/material.dart';

import 'home_page.dart';
import 'link_import.dart';
import 'extras.dart';
import 'model.dart';
import 'monitor_page.dart';
import 'prefs.dart';
import 'proxies_page.dart';
import 'nodes_page.dart';
import 'settings_page.dart';
import 'welcome_page.dart';
import 'widgets.dart';

class VpnApp extends StatelessWidget {
  const VpnApp({super.key, required this.model, this.prefs, this.onQuit});

  final AppModel model;
  final Prefs? prefs;
  final VoidCallback? onQuit;

  @override
  Widget build(BuildContext context) {
    final p = prefs;
    if (p == null) return _app(context, Prefs());
    return ListenableBuilder(listenable: p, builder: (context, _) => _app(context, p));
  }

  Widget _app(BuildContext context, Prefs p) {
    ThemeData theme(Brightness b) {
      var cs = ColorScheme.fromSeed(seedColor: p.accentColor, brightness: b);
      if (b == Brightness.dark && p.amoled) {
        cs = cs.copyWith(surface: Colors.black, surfaceContainerLowest: Colors.black, surfaceContainerLow: const Color(0xFF0A0A0A), surfaceContainer: const Color(0xFF111111), surfaceContainerHigh: const Color(0xFF181818), surfaceContainerHighest: const Color(0xFF202020));
      }
      return ThemeData(
        useMaterial3: true,
        fontFamily: 'Roboto', // свой шрифт из ассетов: одинаковый вид на всех платформах
        fontFamilyFallback: const ['NotoFlags'], // флаги стран: на Windows системный шрифт эмодзи их не рисует
        colorScheme: cs,
        scaffoldBackgroundColor: cs.surface,
        cardTheme: CardThemeData(shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16))),
        inputDecorationTheme: InputDecorationTheme(border: OutlineInputBorder(borderRadius: BorderRadius.circular(12))),
      );
    }

    return MaterialApp(
      title: 'Brod',
      debugShowCheckedModeBanner: false,
      theme: theme(Brightness.light),
      darkTheme: theme(Brightness.dark),
      themeMode: switch (p.themeMode) { 'light' => ThemeMode.light, 'dark' => ThemeMode.dark, _ => ThemeMode.system },
      home: Shell(model: model, prefs: prefs, onQuit: onQuit),
    );
  }
}

/// Оболочка: «Главная» / «Узлы» / «Настройки».
class Shell extends StatefulWidget {
  const Shell({super.key, required this.model, this.prefs, this.onQuit});

  final AppModel model;
  final Prefs? prefs;
  final VoidCallback? onQuit;

  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> {
  int _tab = 0;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModel);
    super.dispose();
  }

  /// Единственное место, где показываются сообщения о результатах действий: забирает сообщение из модели и очищает его, поэтому
  /// оно не повторяется при переходе на другую страницу.
  void _onModel() {
    _offerImport();
    final t = widget.model.takeToast();
    if (t == null) return;
    Future.microtask(() {
      if (mounted) showToast(context, t.$1, error: t.$2);
    });
  }

  bool _importOpen = false;

  /// Ссылка на узел (из другого приложения или из буфера обмена): спрашиваем, добавить ли её в «Мои серверы».
  void _offerImport() {
    final link = widget.model.importOffer;
    if (link == null || _importOpen) return;
    _importOpen = true;
    widget.model.importOffer = null;
    Future.microtask(() async {
      if (!mounted) {
        _importOpen = false;
        return;
      }
      final ok = await showDialog<bool>(
        context: context,
        builder: (ctx) => AlertDialog(
          title: const Text('Добавить сервер?'),
          content: Text('Ссылка на сервер: ${nodeLinkSummary(link)}.\nОн появится в «Моих серверах».'),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Нет')),
            FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Добавить')),
          ],
        ),
      );
      _importOpen = false;
      if (ok == true) await widget.model.addOwn(link);
    });
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        // пока демон не готов, вкладки не показываем
        if (m.phase != Phase.ready) return Scaffold(body: SafeArea(child: HomePage(model: m)));
        // первый запуск: мастер поверх оболочки, пока не пройден или не пропущен
        final pf = widget.prefs;
        if (pf != null && !pf.welcomeDone) return WelcomeFlow(model: m, onDone: () => pf.update(welcomeDone: true));
        // строится только активная вкладка: все пять разом (IndexedStack) перестраивались при каждом обновлении модели, а во время проверки
        // узлов оно приходит каждые несколько секунд; страницы сами запускают свои таймеры при появлении и гасят при уходе
        final pages = KeyedSubtree(
          key: ValueKey(_tab),
          child: switch (_tab) {
            1 => NodesPage(model: m, visible: true, prefs: widget.prefs),
            2 => ProxiesPage(model: m, visible: true),
            3 => MonitorPage(model: m, visible: true),
            4 => SettingsPage(model: m, visible: true, prefs: widget.prefs, onQuit: widget.onQuit),
            _ => HomePage(model: m, onPickNode: () => setState(() => _tab = 1)),
          },
        );
        const dests = [
          (Icons.shield_outlined, Icons.shield, 'Главная'),
          (Icons.dns_outlined, Icons.dns, 'Узлы'),
          (Icons.send_outlined, Icons.send, 'Прокси'),
          (Icons.insights_outlined, Icons.insights, 'Монитор'),
          (Icons.settings_outlined, Icons.settings, 'Настройки'),
        ];
        return LayoutBuilder(builder: (context, c) {
          final wide = c.maxWidth >= 820;
          return Scaffold(
            body: StateGlow(
              model: m,
              child: SafeArea(
                child: wide
                    ? Row(children: [
                        NavigationRail(
                          selectedIndex: _tab,
                          onDestinationSelected: (i) => setState(() => _tab = i),
                          labelType: NavigationRailLabelType.all,
                          destinations: [for (final d in dests) NavigationRailDestination(icon: Icon(d.$1), selectedIcon: Icon(d.$2), label: Text(d.$3))],
                        ),
                        const VerticalDivider(width: 1),
                        Expanded(child: pages),
                      ])
                    : pages,
              ),
            ),
            bottomNavigationBar: wide
                ? null
                : _FloatingNavBar(
                    selectedIndex: _tab,
                    onSelected: (i) => setState(() => _tab = i),
                    destinations: [for (final d in dests) NavigationDestination(icon: Icon(d.$1), selectedIcon: Icon(d.$2), label: d.$3)],
                  ),
          );
        });
      },
    );
  }
}

/// Нижняя панель вкладок на телефоне: «плавающая» таблетка с отступами от краёв. Прижатая к низу полоса упиралась углами в скруглённый экран
/// (крайние вкладки «Главная» и «Настройки» оказывались в самом углу); у таблетки радиус и отступ согласованы со скруглением экрана, а
/// квадратное окно компьютера панель не использует (там боковая колонка).
class _FloatingNavBar extends StatelessWidget {
  const _FloatingNavBar({required this.selectedIndex, required this.onSelected, required this.destinations});

  final int selectedIndex;
  final ValueChanged<int> onSelected;
  final List<NavigationDestination> destinations;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final inset = MediaQuery.of(context).viewPadding.bottom; // жестовая полоса или кнопки системы
    return Padding(
      padding: EdgeInsets.fromLTRB(12, 0, 12, inset > 8 ? inset : 8),
      child: Material(
        color: cs.surfaceContainer,
        elevation: 3,
        shadowColor: Colors.black,
        borderRadius: BorderRadius.circular(28),
        clipBehavior: Clip.antiAlias,
        // отступ снизу уже учтён снаружи: свой SafeArea панели внутри таблетки дал бы пустую полосу под подписями
        child: MediaQuery.removePadding(
          context: context,
          removeBottom: true,
          // подписи мельче и не растягиваются на всю ширину ячейки: «Настройки» иначе упиралась в край таблетки
          child: NavigationBarTheme(
            data: NavigationBarThemeData(labelTextStyle: WidgetStateProperty.resolveWith((states) => states.contains(WidgetState.selected)
                ? TextStyle(fontSize: 11, fontWeight: FontWeight.w700, color: cs.primary)
                : TextStyle(fontSize: 11, fontWeight: FontWeight.w500, color: cs.onSurfaceVariant))),
            child: NavigationBar(
              height: 64,
              backgroundColor: Colors.transparent,
              elevation: 0,
              selectedIndex: selectedIndex,
              onDestinationSelected: onSelected,
              destinations: destinations,
            ),
          ),
        ),
      ),
    );
  }
}
