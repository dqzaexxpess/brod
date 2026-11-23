import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/platform_text.dart';

void main() {
  test('киллсвитч: общая часть одинакова, различия — по платформам', () {
    const common = 'При обрыве VPN трафик блокируется';
    for (final p in Plat.values.where((p) => p != Plat.android)) {
      expect(KillswitchText(p).summary, startsWith(common), reason: '$p: первая фраза одна и та же');
    }
    expect(KillswitchText(Plat.android).summary, startsWith('Трафик без VPN блокируется')); // на телефоне подпись короче: пункт ведёт в настройки системы
    // Linux: реализован, с командами снятия
    final l = KillswitchText(Plat.linux);
    expect(l.supported, true);
    expect(l.howToRelease, contains('brodd --release-killswitch'));
    expect(l.manualCommands, contains('sudo nft delete table inet brod_ks'));
    // Windows: реализован (WFP), снятие через brodctl, без команд nft
    final w = KillswitchText(Plat.windows);
    expect(w.supported, true);
    expect(w.howToRelease, contains('brodctl killswitch release'));
    expect(w.manualCommands, contains('brodctl killswitch release'));
    expect('${w.summary}${w.howToRelease}${w.manualCommands}'.contains('nft'), false);
    // Android/прочее: не реализован, без Linux-команд
    for (final p in [Plat.android, Plat.other]) {
      final k = KillswitchText(p);
      expect(k.supported, false, reason: '$p');
      expect(k.howToRelease, isNull);
      expect(k.manualCommands, isNull);
      expect(k.summary.contains('nft'), false, reason: '$p: команды nft только для Linux');
    }
    expect(KillswitchText(Plat.android).summary, contains('Постоянный VPN'));
    expect(KillswitchText(Plat.windows).summary, contains('локальная сеть'));
  });

  test('отчёт: Windows 11 определяется по номеру сборки, а не по названию из реестра', () {
    expect(osDescription('windows', '"Windows 10 Pro" 10.0 (Build 22000)'), 'windows "Windows 11 Pro" 10.0 (Build 22000)');
    expect(osDescription('windows', '"Windows 10 Pro" 10.0 (Build 26200)'), 'windows "Windows 11 Pro" 10.0 (Build 26200)');
    expect(osDescription('windows', '"Windows 10 Pro" 10.0 (Build 19045)'), 'windows "Windows 10 Pro" 10.0 (Build 19045)', reason: 'настоящая «десятка» остаётся');
    expect(osDescription('windows', '"Windows 11 Pro" 10.0 (Build 22631)'), 'windows "Windows 11 Pro" 10.0 (Build 22631)');
    expect(osDescription('windows', 'что-то непонятное'), 'windows что-то непонятное');
    expect(osDescription('linux', 'Linux 6.14'), 'linux Linux 6.14');
  });
}
