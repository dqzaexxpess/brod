import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:qr_flutter/qr_flutter.dart';

/// Самая длинная ссылка, которую ещё можно уместить в QR-код читаемым размером (предел формата ~2950 байт; с запасом на кириллицу и камеру).
const qrMaxLink = 1800;

/// Показывает ссылку QR-кодом: второе устройство наводит штатную камеру, и ссылка открывается в Brod. [secret] — в ссылке пароль или ключ.
Future<void> showQrDialog(BuildContext context, {required String title, required String link, bool secret = false}) {
  final tooLong = link.length > qrMaxLink;
  return showDialog<void>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: Text(title),
      content: SizedBox(
        width: 300,
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          if (tooLong)
            const Text('Ссылка слишком длинная для QR-кода. Передайте её текстом: «Поделиться» или «Скопировать ссылку».')
          else
            // белая подложка нужна и в тёмной теме: камера не читает светлый код на тёмном фоне
            Container(
              color: Colors.white,
              padding: const EdgeInsets.all(8),
              child: QrImageView(data: link, size: 280, errorCorrectionLevel: QrErrorCorrectLevel.L, backgroundColor: Colors.white),
            ),
          if (secret && !tooLong)
            const Padding(padding: EdgeInsets.only(top: 12), child: Text('В коде пароль или ключ сервера: не показывайте его посторонним.', style: TextStyle(fontSize: 12))),
        ]),
      ),
      actions: [
        TextButton(
          onPressed: () async {
            await Clipboard.setData(ClipboardData(text: link));
            if (ctx.mounted) Navigator.pop(ctx);
          },
          child: const Text('Скопировать ссылку'),
        ),
        TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Закрыть')),
      ],
    ),
  );
}
