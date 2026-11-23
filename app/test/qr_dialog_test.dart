import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:qr_flutter/qr_flutter.dart';
import 'package:brod/src/qr_dialog.dart';

Future<void> _open(WidgetTester t, String link, {bool secret = false}) async {
  await t.pumpWidget(MaterialApp(home: Scaffold(body: Builder(builder: (c) => TextButton(onPressed: () => showQrDialog(c, title: 'QR', link: link, secret: secret), child: const Text('go'))))));
  await t.tap(find.text('go'));
  await t.pumpAndSettle();
}

void main() {
  testWidgets('QR показывается для обычной ссылки; для секретной есть предупреждение', (t) async {
    await _open(t, 'vless://u@h.example:443#x', secret: true);
    expect(find.byType(QrImageView), findsOneWidget);
    expect(find.textContaining('не показывайте его посторонним'), findsOneWidget);
  });

  testWidgets('слишком длинная ссылка: вместо кода пояснение, копирование остаётся', (t) async {
    await _open(t, 'brod://route/${'a' * (qrMaxLink + 10)}');
    expect(find.byType(QrImageView), findsNothing);
    expect(find.textContaining('слишком длинная для QR'), findsOneWidget);
    expect(find.text('Скопировать ссылку'), findsOneWidget);
  });
}
