import 'package:flutter/material.dart';

import 'model.dart';

/// Мастер первого запуска: что такое Brod, состояние WARP (на Android аккаунт создаётся сам при первом запуске, а без работающего
/// WARP живо около 1% узлов), выбор узла и краткий обзор экранов. Показывается один раз; «Пропустить» доступно на каждом шаге.
class WelcomeFlow extends StatefulWidget {
  const WelcomeFlow({super.key, required this.model, required this.onDone});

  final AppModel model;
  final VoidCallback onDone;

  @override
  State<WelcomeFlow> createState() => _WelcomeFlowState();
}

class _WelcomeFlowState extends State<WelcomeFlow> {
  static const _steps = 4;
  int _i = 0;

  AppModel get m => widget.model;

  @override
  void initState() {
    super.initState();
    m.loadWarp();
  }

  void _next() => _i == _steps - 1 ? widget.onDone() : setState(() => _i++);

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Scaffold(
      body: SafeArea(
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 520),
            child: Padding(
              padding: const EdgeInsets.fromLTRB(24, 16, 24, 24),
              child: Column(children: [
                Row(children: [
                  for (var k = 0; k < _steps; k++)
                    Expanded(
                      child: Container(
                        height: 4,
                        margin: const EdgeInsets.only(right: 6),
                        decoration: BoxDecoration(color: k <= _i ? cs.primary : cs.surfaceContainerHighest, borderRadius: BorderRadius.circular(2)),
                      ),
                    ),
                  TextButton(onPressed: widget.onDone, child: const Text('Пропустить')),
                ]),
                Expanded(
                  child: ListenableBuilder(
                    listenable: m,
                    builder: (context, _) => SingleChildScrollView(
                      padding: const EdgeInsets.symmetric(vertical: 16),
                      child: switch (_i) {
                        0 => _intro(context),
                        1 => _warp(context),
                        2 => _pick(context),
                        _ => _tour(context),
                      },
                    ),
                  ),
                ),
                Row(children: [
                  if (_i > 0) OutlinedButton(onPressed: () => setState(() => _i--), child: const Text('Назад')),
                  const Spacer(),
                  FilledButton(onPressed: _next, child: Text(_i == _steps - 1 ? 'Готово' : 'Дальше')),
                ]),
              ]),
            ),
          ),
        ),
      ),
    );
  }

  Widget _head(BuildContext context, IconData icon, String title) {
    final cs = Theme.of(context).colorScheme;
    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Icon(icon, size: 44, color: cs.primary),
        const SizedBox(height: 12),
        Text(title, style: Theme.of(context).textTheme.headlineSmall),
      ]),
    );
  }

  Widget _intro(BuildContext context) => Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        _head(context, Icons.shield_outlined, 'Добро пожаловать в Brod'),
        const Text('Brod собирает бесплатные прокси-подписки в одну базу, проверяет узлы в вашей сети и подключает тот, что работает лучше всего.'),
        const SizedBox(height: 12),
        const Text('Узлов в базе тысячи, большинство из них не работает. Проверки как раз и отделяют живые от мёртвых: на это нужно время, поэтому первые минуты после установки список заполняется постепенно.'),
        const SizedBox(height: 16),
        Card.filled(
          color: Theme.of(context).colorScheme.tertiaryContainer,
          child: Padding(
            padding: const EdgeInsets.all(14),
            child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Icon(Icons.warning_amber_rounded, color: Theme.of(context).colorScheme.onTertiaryContainer),
              const SizedBox(width: 12),
              Expanded(
                child: Text(
                  'Серверы из бесплатных подписок чужие. Их владелец видит, на какие сайты вы заходите, а на сайтах без шифрования (http://) может читать и подменять то, что вы отправляете и получаете. Не вводите через такие серверы пароли на сайтах без https и не входите в банки; для важного используйте свой сервер или обычное соединение.',
                  style: TextStyle(color: Theme.of(context).colorScheme.onTertiaryContainer),
                ),
              ),
            ]),
          ),
        ),
      ]);

  Widget _warp(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final s = m.warpState;
    final enabled = s?.enabled ?? m.status?.warpEnabled == true;
    final down = s?.down == true;
    final scanning = m.warpScanning || s?.scanning == true;
    final (IconData icon, String text, Color color) = switch ((s == null, enabled, down)) {
      (true, _, _) => (Icons.hourglass_empty, 'Проверяю…', cs.onSurfaceVariant),
      (_, false, _) => (Icons.cloud_off, 'Аккаунт WARP не создался (нет связи с Cloudflare). Проверки будут идти напрямую, и живыми окажется очень мало узлов.', cs.error),
      (_, _, true) => (Icons.cloud_off, 'WARP не отвечает в вашей сети. Пока он не заработает, живыми будут около 1% узлов.', cs.error),
      _ => (Icons.cloud_done_outlined, 'WARP работает: аккаунт создан автоматически, проверки идут через него.', cs.primary),
    };
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      _head(context, Icons.cloud_outlined, 'Cloudflare WARP'),
      const Text('Напрямую большинство узлов недоступно: сеть их режет. Через WARP живых узлов заметно больше, поэтому Brod ходит к узлам цепочкой «WARP → узел». Регистрироваться самому не нужно: аккаунт создаётся при первом запуске.'),
      const SizedBox(height: 16),
      Card.filled(
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Icon(icon, color: color),
            const SizedBox(width: 12),
            Expanded(child: Text(text)),
          ]),
        ),
      ),
      const SizedBox(height: 12),
      if (s != null && (!enabled || down)) ...[
        if (enabled)
          FilledButton.icon(
            icon: scanning ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.travel_explore),
            label: Text(scanning ? 'Подбираю… (до нескольких минут)' : 'Подобрать для этой сети'),
            onPressed: scanning ? null : m.scanWarp,
          )
        else
          FilledButton.icon(
            icon: m.warpBusy ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.refresh),
            label: Text(m.warpBusy ? 'Создаю…' : 'Создать аккаунт ещё раз'),
            onPressed: m.warpBusy ? null : () async { await m.warpRegister(); await m.loadWarp(); },
          ),
        const SizedBox(height: 8),
        Text('Подбор пробует другие адреса, порты и маскировки WARP и запоминает рабочий вариант для этой сети. Позже его можно повторить в Настройках → Cloudflare WARP.', style: Theme.of(context).textTheme.bodySmall),
      ],
    ]);
  }

  Widget _pick(BuildContext context) => Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        _head(context, Icons.auto_awesome, 'Выбор узла'),
        const Text('Нажмите кнопку ниже, и Brod выберет самый быстрый из проверенных узлов. Если живых пока нет, подождите минуту-две и повторите: на вкладке «Узлы» видно, как идёт проверка.'),
        const SizedBox(height: 16),
        FilledButton.icon(
          onPressed: m.bestBusy ? null : m.connectBest,
          icon: m.bestBusy ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.auto_awesome),
          label: const Text('Подключиться к лучшему узлу'),
        ),
        const SizedBox(height: 12),
        Text('Узел можно сменить в любой момент: вкладка «Узлы» или меню ⋮ на главной.', style: Theme.of(context).textTheme.bodySmall),
        const SizedBox(height: 12),
        const Text('Если выбранный узел перестанет отвечать, Brod пришлёт уведомление с предложением, а менять узел вы будете сами. Чтобы он менялся автоматически на проверенный, включите «Переключать сам» в Настройки → «Если выход перестал отвечать».'),
      ]);

  Widget _tour(BuildContext context) {
    Widget row(IconData i, String t, String d) => ListTile(contentPadding: EdgeInsets.zero, leading: Icon(i), title: Text(t), subtitle: Text(d));
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      _head(context, Icons.explore_outlined, 'Что где'),
      row(Icons.shield, 'Главная', 'Включить VPN, режим («Только заблокированное», «Всё»), активный узел и его проверки: пинг, скорость, «для звонков», чистота IP.'),
      row(Icons.dns, 'Узлы', 'Вся база с метками работоспособности; закреплять, сортировать, проверять вручную.'),
      row(Icons.send, 'Прокси', 'Свои узлы и ссылки: вставить vless://, vmess://, подписку или импорт из другого клиента.'),
      row(Icons.insights, 'Монитор', 'Какие соединения идут и через какой выход; можно отправить соединение «через выход».'),
      row(Icons.settings, 'Настройки', 'Источники подписок, правила, WARP, обновления, диагностика.'),
    ]);
  }
}
