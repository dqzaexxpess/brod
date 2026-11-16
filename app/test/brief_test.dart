import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/brief.dart';

void main() {
  test('строка «Режим · Протокол · Страна»: части без данных пропускаются', () {
    const v = VpnState(on: true, mode: 'all');
    const full = ActiveNode(proto: 'vless', server: 'h', port: 443, country: 'NL');
    expect(briefLine(v, full), 'Всё через выход · VLESS · 🇳🇱 Нидерланды');
    expect(briefLine(v, full, flag: false), 'Всё через выход · VLESS · Нидерланды', reason: 'в трее флагов-эмодзи нет');
    expect(briefLine(v, const ActiveNode(proto: 'ss', server: 'h', port: 1)), 'Всё через выход · Shadowsocks'); // страна ещё не определена
    expect(briefLine(v, const ActiveNode(proto: 'warp', server: 'w', port: 0, country: 'XX')), 'Всё через выход · WARP');
    expect(briefLine(null, null), '');
    expect(protoName('hy2'), 'Hysteria2');
    expect(protoName('anytls'), 'ANYTLS'); // неизвестный протокол — прописными
  });
}
