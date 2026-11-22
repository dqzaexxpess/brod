import 'dart:io';

import 'package:flutter/material.dart';

import 'setup_args.dart';
import 'setup_log.dart';
import 'setup_runner.dart';

/// Тёмная тема в цветах Brod (тот же исходный цвет и шрифт, что у приложения).
ThemeData setupTheme() {
  final cs = ColorScheme.fromSeed(seedColor: const Color(0xFF2E9E6B), brightness: Brightness.dark);
  return ThemeData(
    useMaterial3: true,
    fontFamily: 'Roboto',
    colorScheme: cs,
    scaffoldBackgroundColor: const Color(0xFF111114),
    inputDecorationTheme: InputDecorationTheme(border: OutlineInputBorder(borderRadius: BorderRadius.circular(12))),
  );
}

String defaultAccount([Map<String, String>? env]) {
  final e = env ?? Platform.environment;
  final user = e['USERNAME'] ?? '';
  final domain = e['USERDOMAIN'] ?? '';
  return user.isEmpty ? '' : (domain.isEmpty ? user : '$domain\\$user');
}

enum _Stage { welcome, running, done, failed }

/// Мастер установки и удаления в оформлении Brod. Привилегированную работу делает [runner]; окно само прав не повышает.
class SetupWizard extends StatefulWidget {
  const SetupWizard({
    super.key,
    required this.mode,
    required this.runner,
    required this.installDir,
    required this.onExit,
    required this.launchApp,
    this.account,
    this.version = '',
  });

  final SetupMode mode;
  final SetupRunner runner;
  final String installDir;
  final void Function(int code) onExit;
  final Future<void> Function(String dir) launchApp; // запуск установленного Brod от обычного пользователя
  final String? account;
  final String version;

  @override
  State<SetupWizard> createState() => _SetupWizardState();
}

class _SetupWizardState extends State<SetupWizard> {
  _Stage _stage = _Stage.welcome;
  late final TextEditingController _account = TextEditingController(text: widget.account ?? defaultAccount());
  bool _removeData = false;
  bool _launch = true;
  final _lines = <String>[];
  SetupOutcome? _outcome;

  bool get _install => widget.mode == SetupMode.install;

  Future<void> _run() async {
    if (_install && _account.text.trim().isEmpty) return;
    setState(() {
      _stage = _Stage.running;
      _lines.clear();
    });
    void onLine(String l) {
      if (mounted) setState(() => _lines.add(l));
    }

    setupLog('${_install ? 'установка' : 'удаление'}: каталог ${widget.installDir}');
    SetupOutcome out;
    try {
      out = _install
          ? await widget.runner.install(account: _account.text.trim(), dir: widget.installDir, onLine: onLine)
          : await widget.runner.uninstall(dir: widget.installDir, removeData: _removeData, onLine: onLine);
    } catch (e, st) {
      // PowerShell не запустился и т. п.: показать причину, а не оставить окно на странице хода
      setupLog('шаг не выполнен: $e\n$st');
      out = SetupOutcome(1, ['Не удалось запустить PowerShell: $e']);
    }
    setupLog('итог: код ${out.exitCode}${out.lines.isEmpty ? '' : ', последняя строка: ${out.lines.last}'}');
    if (!mounted) return;
    setState(() {
      _outcome = out;
      _stage = out.ok ? _Stage.done : _Stage.failed;
    });
  }

  Future<void> _finish() async {
    if (_install && _launch && _outcome?.ok == true) await widget.launchApp(widget.installDir);
    widget.onExit(0);
  }

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Scaffold(
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(36, 28, 36, 28),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              Image.asset('assets/app_icon.png', width: 44, height: 44),
              const SizedBox(width: 14),
              Text('Brod', style: Theme.of(context).textTheme.headlineMedium?.copyWith(fontWeight: FontWeight.w700, letterSpacing: 1)),
              if (widget.version.isNotEmpty) ...[
                const SizedBox(width: 10),
                Text(widget.version, style: TextStyle(color: cs.onSurfaceVariant)),
              ],
            ]),
            const SizedBox(height: 28),
            Expanded(child: _body(context)),
          ]),
        ),
      ),
    );
  }

  Widget _body(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    final title = Theme.of(context).textTheme.titleLarge;
    final sub = TextStyle(color: cs.onSurfaceVariant);
    switch (_stage) {
      case _Stage.welcome:
        return _install ? _welcomeInstall(title, sub) : _welcomeUninstall(title, sub);
      case _Stage.running:
        return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(_install ? 'Устанавливаю…' : 'Удаляю…', style: title),
          const SizedBox(height: 18),
          const LinearProgressIndicator(),
          const SizedBox(height: 18),
          Expanded(child: _logView(sub)),
        ]);
      case _Stage.done:
        return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Icon(Icons.check_circle_outline, color: cs.primary, size: 40),
          const SizedBox(height: 14),
          Text(_install ? 'Brod установлен' : 'Brod удалён', style: title),
          const SizedBox(height: 8),
          Text(_install ? 'Служба запущена, ярлык есть в меню «Пуск».' : (_removeData ? 'База узлов и настройки удалены.' : 'База узлов и настройки сохранены: они пригодятся при повторной установке.'), style: sub),
          const Spacer(),
          if (_install)
            CheckboxListTile(contentPadding: EdgeInsets.zero, controlAffinity: ListTileControlAffinity.leading, value: _launch, onChanged: (v) => setState(() => _launch = v ?? true), title: const Text('Запустить Brod')),
          const SizedBox(height: 8),
          Align(alignment: Alignment.centerRight, child: FilledButton(onPressed: _finish, child: const Text('Готово'))),
        ]);
      case _Stage.failed:
        final o = _outcome!;
        return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Icon(Icons.error_outline, color: cs.error, size: 40),
          const SizedBox(height: 14),
          Text(o.cancelled ? 'Нужны права администратора' : (_install ? 'Не удалось установить' : 'Не удалось удалить'), style: title),
          const SizedBox(height: 8),
          Text(o.cancelled ? 'Запрос Windows на повышение прав был отклонён. Brod не изменён.' : (failureSummary(o.lines).isEmpty ? 'Код ${o.exitCode}.' : failureSummary(o.lines)), style: sub.copyWith(color: cs.onSurface)),
          const SizedBox(height: 14),
          if (!o.cancelled) Expanded(child: _details(o, sub)) else const Spacer(),
          const SizedBox(height: 8),
          Row(mainAxisAlignment: MainAxisAlignment.end, children: [
            TextButton(onPressed: () => widget.onExit(o.exitCode == 0 ? 1 : o.exitCode), child: const Text('Закрыть')),
            const SizedBox(width: 8),
            TextButton(onPressed: () => setState(() => _stage = _Stage.welcome), child: const Text('Назад')),
            const SizedBox(width: 8),
            FilledButton(onPressed: _run, child: const Text('Повторить')),
          ]),
        ]);
    }
  }

  Widget _welcomeInstall(TextStyle? title, TextStyle sub) => Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text('Установка', style: title),
        const SizedBox(height: 8),
        Text('Brod установится в ${widget.installDir} как служба Windows. Подтвердите права администратора, когда Windows спросит.', style: sub),
        const SizedBox(height: 22),
        TextField(controller: _account, decoration: const InputDecoration(labelText: 'Учётная запись пользователя Brod', helperText: 'Она получит доступ к службе и право её остановить. Обычно это ваша.', helperMaxLines: 2)),
        const Spacer(),
        Align(alignment: Alignment.centerRight, child: FilledButton(onPressed: _run, child: const Text('Установить'))),
      ]);

  Widget _welcomeUninstall(TextStyle? title, TextStyle sub) => Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text('Удаление', style: title),
        const SizedBox(height: 8),
        Text('Служба Brod и файлы программы будут удалены из ${widget.installDir}.', style: sub),
        const SizedBox(height: 18),
        CheckboxListTile(
          contentPadding: EdgeInsets.zero,
          controlAffinity: ListTileControlAffinity.leading,
          value: _removeData,
          onChanged: (v) => setState(() => _removeData = v ?? false),
          title: const Text('Удалить также базу узлов, настройки и токены'),
          subtitle: Text('Если оставить, они пригодятся при повторной установке.', style: sub),
        ),
        const Spacer(),
        Row(mainAxisAlignment: MainAxisAlignment.end, children: [
          TextButton(onPressed: () => widget.onExit(0), child: const Text('Отмена')),
          const SizedBox(width: 8),
          FilledButton(onPressed: _run, child: const Text('Удалить')),
        ]),
      ]);

  /// Подробности ошибки свёрнуты: под фразой остаётся протокол без строк, которые уже сказаны в ней.
  Widget _details(SetupOutcome o, TextStyle sub) {
    final summary = failureSummary(o.lines).toLowerCase();
    // строки, уже сказанные во фразе, и их обрывки: консоль PowerShell переносит длинную строку на ширине окна, и хвост («мя (параметр -User).»)
    // попадает в протокол отдельной строкой
    final rest = [for (final l in o.lines) if (summary.isEmpty || !(summary.contains(l.toLowerCase()) || l.toLowerCase().contains(summary.substring(0, summary.length.clamp(0, 30))))) l];
    if (rest.isEmpty) return const SizedBox.shrink();
    return SingleChildScrollView(
      child: ExpansionTile(
        tilePadding: EdgeInsets.zero,
        childrenPadding: EdgeInsets.zero,
        expandedAlignment: Alignment.centerLeft,
        title: Text('Подробности', style: sub),
        children: [SelectableText(rest.join('\n'), style: sub.copyWith(fontSize: 12.5, height: 1.4))],
      ),
    );
  }

  Widget _logView(TextStyle sub, {int last = 8}) {
    final tail = _lines.length > last ? _lines.sublist(_lines.length - last) : _lines;
    return SingleChildScrollView(
      reverse: true,
      child: SelectableText(tail.join('\n'), style: sub.copyWith(fontSize: 12.5, height: 1.4)),
    );
  }
}
