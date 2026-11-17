import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'api.dart';
import 'format.dart';
import 'model.dart';

/// Подсказка, как поставить скачанный и проверенный файл: установку приложение само не делает (на Android она требует подтверждения системы,
/// на Linux и Windows — прав). Платформа задаётся для тестов.
String updateInstallHint({bool? android, bool? windows}) {
  if (android ?? Platform.isAndroid) {
    return 'Файл проверен; установите его кнопкой выше.';
  }
  if (windows ?? Platform.isWindows) {
    return 'Файл проверен: запустите установщик Brod от имени администратора.';
  }
  return 'Установка вручную: распакуйте архив и запустите установку (нужен sudo).';
}

/// Понятная фраза вместо сырой ошибки сети или проверки (сырой текст остаётся в «Подробностях»).
String friendlyUpdateError(String raw) {
  final r = raw.toLowerCase();
  if (r.contains('откат')) return 'Сервер отдал список релизов старее уже виденного, он отвергнут.';
  if (r.contains('подпис') || r.contains('отвергнут') || r.contains('не прошёл проверку') || r.contains('sha-256') || r.contains('sha256')) {
    return 'Данные обновления не прошли проверку подписи или контрольной суммы.';
  }
  if (r.contains('no such host') || r.contains('lookup ') || r.contains('dns')) return 'Сервер обновлений не найден: проблема с DNS или он недоступен из этой сети.';
  if (r.contains('connection reset') || r.contains('connection refused') || r.contains('timeout') || r.contains('deadline') || r.contains('eof') || r.contains('unreachable') || r.contains('tls')) {
    return 'Сервер обновлений не отвечает или соединение оборвалось.';
  }
  final m = RegExp(r'http (\d{3})').firstMatch(r);
  if (m != null) return 'Сервер обновлений ответил ошибкой (HTTP ${m.group(1)}).';
  if (r.contains('нужен https')) return 'Адрес обновлений задан неверно (нужен https).';
  return 'Проверка не удалась.';
}

Widget _errorBlock(BuildContext context, String lead, String raw) {
  final cs = Theme.of(context).colorScheme;
  return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
    Text('$lead ${friendlyUpdateError(raw)}', style: TextStyle(color: cs.error)),
    Theme(
      data: Theme.of(context).copyWith(dividerColor: Colors.transparent),
      child: ExpansionTile(
        tilePadding: EdgeInsets.zero,
        dense: true,
        title: Text('Подробности', style: Theme.of(context).textTheme.bodySmall),
        children: [Align(alignment: Alignment.centerLeft, child: SelectableText(raw, style: Theme.of(context).textTheme.bodySmall))],
      ),
    ),
  ]);
}

Future<void> showUpdateDialog(BuildContext context, AppModel model) => showDialog<void>(context: context, builder: (_) => UpdateDialog(model: model));

/// Обновление приложения: что установлено, что доступно, проверка, скачивание с проверкой размера и SHA-256 по подписанному манифесту.
class UpdateDialog extends StatefulWidget {
  const UpdateDialog({super.key, required this.model});

  final AppModel model;

  @override
  State<UpdateDialog> createState() => _UpdateDialogState();
}

class _UpdateDialogState extends State<UpdateDialog> {
  @override
  void initState() {
    super.initState();
    Future.microtask(widget.model.loadUpdate);
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.model;
    return ListenableBuilder(
      listenable: m,
      builder: (context, _) {
        final u = m.updateInfo;
        final cs = Theme.of(context).colorScheme;
        final small = Theme.of(context).textTheme.bodySmall;
        final dl = u?.download ?? const UpdateDownloadDto();
        return AlertDialog(
          title: const Text('Обновление приложения'),
          content: SizedBox(
            width: 440,
            child: SingleChildScrollView(
              child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
                Text(u == null ? 'Загрузка…' : 'Установлена версия ${u.current}${u.platform.isEmpty ? '' : ' (${u.platform})'}'),
                if (u?.last != null && showLastInstall(u!))
                  Padding(
                    padding: const EdgeInsets.only(top: 6),
                    child: Text(
                      u.last!.ok ? 'Последняя установка: ${u.last!.from} → ${u.last!.to} выполнена.' : 'Последняя установка ${u.last!.from} → ${u.last!.to} не удалась: ${installErrorText(u.last!.error)}',
                      style: TextStyle(color: u.last!.ok ? cs.primary : cs.error),
                    ),
                  ),
                if (m.updateBusy) const Padding(padding: EdgeInsets.only(top: 8), child: LinearProgressIndicator(minHeight: 2)),
                if (u != null && u.available) ...[
                  const SizedBox(height: 12),
                  Text('Доступна версия ${u.app}', style: Theme.of(context).textTheme.titleMedium),
                  if (u.notes.isNotEmpty) Padding(padding: const EdgeInsets.only(top: 4), child: Text(u.notes)),
                  Padding(padding: const EdgeInsets.only(top: 4), child: Text('Файл: ${fmtBytes(u.size)}', style: small)),
                  const SizedBox(height: 12),
                  if (dl.downloading) ...[
                    LinearProgressIndicator(value: dl.total > 0 ? dl.bytes / dl.total : null),
                    Padding(padding: const EdgeInsets.only(top: 4), child: Text('Скачано ${fmtBytes(dl.bytes)} из ${fmtBytes(dl.total)}', style: small)),
                  ] else if (dl.ready) ...[
                    Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
                      Icon(Icons.verified_outlined, color: cs.primary),
                      const SizedBox(width: 8),
                      Expanded(child: Text('Файл скачан и проверен: размер и SHA-256 совпали с подписанным манифестом.', style: TextStyle(color: cs.primary))),
                    ]),
                    const SizedBox(height: 6),
                    Row(children: [
                      Expanded(child: SelectableText(dl.path, style: small)),
                      IconButton(
                        tooltip: 'Скопировать путь',
                        icon: const Icon(Icons.copy, size: 18),
                        onPressed: () async {
                          await Clipboard.setData(ClipboardData(text: dl.path));
                          if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Путь скопирован')));
                        },
                      ),
                    ]),
                    if (m.installSupported) ...[
                      const SizedBox(height: 8),
                      FilledButton.icon(
                        icon: const Icon(Icons.system_update_alt),
                        label: const Text('Установить'),
                        onPressed: m.updateInstalling ? null : m.installUpdate,
                      ),
                      if (m.updateNote.isNotEmpty) Padding(padding: const EdgeInsets.only(top: 6), child: Text(m.updateNote, style: small)),
                    ] else
                      Text(updateInstallHint(), style: small),
                  ] else ...[
                    if (dl.failed) Padding(padding: const EdgeInsets.only(bottom: 8), child: _errorBlock(context, 'Скачивание не удалось.', dl.error)),
                    if (u.metered)
                      Padding(
                        padding: const EdgeInsets.only(bottom: 8),
                        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
                          Icon(Icons.signal_cellular_alt, size: 18, color: cs.tertiary),
                          const SizedBox(width: 8),
                          Expanded(child: Text('Сейчас мобильная сеть или платный трафик: файл ${fmtBytes(u.size)} спишется с пакета. Лучше подождать Wi-Fi.', style: small)),
                        ]),
                      ),
                    FilledButton.icon(
                      icon: const Icon(Icons.download),
                      label: Text(u.metered ? 'Скачать по мобильной сети (${fmtBytes(u.size)})' : (dl.failed ? 'Повторить' : 'Скачать и проверить')),
                      onPressed: () => m.downloadUpdate(allowMetered: u.metered),
                    ),
                  ],
                ] else if (u != null && u.checked != null && u.error.isEmpty) ...[
                  const SizedBox(height: 12),
                  Text('Обновлений нет. Проверено ${fmtAge(u.checked!)} назад.'),
                ] else if (u != null && u.checked == null && u.error.isEmpty) ...[
                  const SizedBox(height: 12),
                  Text('Обновления проверяются раз в сутки.', style: small),
                ],
                if (u != null && u.error.isNotEmpty) Padding(padding: const EdgeInsets.only(top: 12), child: _errorBlock(context, 'Проверка не удалась.', u.error)),
              ]),
            ),
          ),
          actions: [
            // во время скачивания и установки повторная проверка ничего не прерывает, поэтому кнопки нет
            if (!dl.downloading && u?.installing != true) TextButton(onPressed: m.updateBusy ? null : m.checkUpdate, child: const Text('Проверить сейчас')),
            TextButton(onPressed: () => Navigator.pop(context), child: const Text('Закрыть')),
          ],
        );
      },
    );
  }
}

/// Плашка «Доступна версия» на главном экране (показывается, пока в статусе есть блок update).
class UpdateBanner extends StatelessWidget {
  const UpdateBanner({super.key, required this.model});

  final AppModel model;

  @override
  Widget build(BuildContext context) {
    final u = model.status?.update;
    if (u == null) return const SizedBox.shrink();
    final cs = Theme.of(context).colorScheme;
    return Card.filled(
      color: cs.primaryContainer,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 8, 8, 8),
        child: Row(children: [
          Icon(Icons.system_update_alt, color: cs.onPrimaryContainer),
          const SizedBox(width: 10),
          Expanded(child: Text('Доступна версия ${u.app}${u.download == 'ready' ? ' · скачана и проверена' : ''}', style: TextStyle(color: cs.onPrimaryContainer))),
          TextButton(onPressed: () => showUpdateDialog(context, model), child: const Text('Подробнее')),
        ]),
      ),
    );
  }
}


/// Текст ошибки установки без шума: pkexec дописывает «This incident has been reported.» (это запись в локальный журнал системы, никуда не
/// отправляется), а отказ polkit по-английски заменяется понятной причиной.
String installErrorText(String raw) {
  if (raw.contains('Not authorized')) {
    return 'система не выдала права администратора (polkit: Not authorized); нужен графический polkit-агент в сеансе, запустите установку из приложения';
  }
  final lines = [
    for (final l in raw.split('\n'))
      if (l.trim().isNotEmpty && !l.trim().toLowerCase().startsWith('this incident has been reported')) l.trim(),
  ];
  return lines.join(' ');
}

/// Итог прошлой установки показывается, пока он относится к тому же, что предлагается сейчас: неудача установки другой версии (например,
/// 0.3.1) при доступной 0.4.0 только пугает. Успех и неудача без новой версии остаются видны.
bool showLastInstall(UpdateDto u) {
  final l = u.last;
  if (l == null) return false;
  if (l.ok || !u.available) return true;
  return l.to == u.app;
}
