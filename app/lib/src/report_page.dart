import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'android_bridge.dart';
import 'model.dart';

/// «Диагностический отчёт»: одна кнопка запускает тесты ядра (протоколы и причины провалов, DNS, связь, WARP, пробы узлов), а итог уходит
/// одним файлом тому, кто разбирает неполадку. Адреса, имена узлов и секреты в отчёт не попадают.
class ReportPage extends StatelessWidget {
  const ReportPage({super.key, required this.model});

  final AppModel model;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final cs = Theme.of(context).colorScheme;
        final small = Theme.of(context).textTheme.bodySmall;
        final r = model.report;
        final vpnOn = model.vpn?.on == true;
        return Scaffold(
          appBar: AppBar(title: const Text('Диагностический отчёт')),
          body: ListView(padding: const EdgeInsets.all(16), children: [
            const Text('Тесты идут около минуты: проверка DNS (системный, DoH, через WARP), прямой связи, WARP и выборочных узлов каждого протокола. '
                'Потом файл можно отправить разработчику. В нём нет адресов серверов, имён узлов и ключей.'),
            const SizedBox(height: 8),
            const Text('Запускайте через 10–15 минут после первого запуска приложения, с открытым экраном и выключенным VPN: сразу после установки проверки узлов ещё не прошли, и отчёт получится почти пустым.'),
            if (vpnOn) ...[
              const SizedBox(height: 12),
              Card.filled(
                color: cs.tertiaryContainer,
                child: Padding(
                  padding: const EdgeInsets.all(12),
                  child: Text('VPN включён: прямые тесты пойдут через него и покажут не то. Выключите VPN на главной и запустите снова.', style: TextStyle(color: cs.onTertiaryContainer)),
                ),
              ),
            ],
            const SizedBox(height: 16),
            FilledButton.icon(
              icon: model.reportRunning ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.fact_check_outlined),
              label: Text(model.reportRunning ? 'Идут тесты… ${r?.stage ?? ''}' : 'Запустить тесты'),
              onPressed: model.reportRunning ? null : model.runReport,
            ),
            if (r != null && r.state == 'failed') ...[
              const SizedBox(height: 12),
              Text('Отчёт не собрался: ${r.error}', style: TextStyle(color: cs.error)),
            ],
            if (r != null && r.done) ...[
              const SizedBox(height: 20),
              Text('Выводы', style: Theme.of(context).textTheme.titleMedium),
              const SizedBox(height: 6),
              for (final f in r.findings)
                Padding(padding: const EdgeInsets.only(bottom: 6), child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  const Text('• '),
                  Expanded(child: Text(f)),
                ])),
              const SizedBox(height: 4),
              Text('Это подсказки по правилам, а не приговор: сами данные в файле.', style: small),
              const SizedBox(height: 16),
              Wrap(spacing: 12, runSpacing: 8, children: [
                FilledButton.icon(
                  icon: Icon(Platform.isAndroid ? Icons.share : Icons.save_alt),
                  label: Text(Platform.isAndroid ? 'Отправить отчёт' : 'Сохранить файл'),
                  onPressed: () => _send(context),
                ),
                OutlinedButton.icon(
                  icon: const Icon(Icons.copy),
                  label: const Text('Копировать текст'),
                  onPressed: () async {
                    await Clipboard.setData(ClipboardData(text: r.text));
                    if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Скопировано')));
                  },
                ),
              ]),
            ],
          ]),
        );
      },
    );
  }

  Future<void> _send(BuildContext context) async {
    final r = model.report;
    if (r == null) return;
    final messenger = ScaffoldMessenger.of(context);
    try {
      if (Platform.isAndroid) {
        await AndroidCore.shareReport(r.text, jsonEncode(r.json));
      } else {
        final path = await model.saveReport();
        messenger.showSnackBar(SnackBar(content: Text('Сохранено: $path')));
      }
    } catch (e) {
      messenger.showSnackBar(SnackBar(content: Text('Не получилось: $e')));
    }
  }
}
