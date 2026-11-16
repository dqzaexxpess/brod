import 'dart:io';

/// Платформа, для которой подбираются формулировки (в тестах подменяется).
enum Plat { linux, windows, android, other }

Plat? platformOverride;

Plat get currentPlat {
  if (platformOverride != null) return platformOverride!;
  if (Platform.isLinux) return Plat.linux;
  if (Platform.isWindows) return Plat.windows;
  if (Platform.isAndroid) return Plat.android;
  return Plat.other;
}

/// Тексты про киллсвитч — один источник для настроек и экрана блокировки, с отличиями по платформам.
/// Общая часть одинакова везде; различается только то, как работает и как снимается.
class KillswitchText {
  const KillswitchText(this.p);

  KillswitchText.current() : p = currentPlat;

  final Plat p;

  /// Реализован ли киллсвитч на платформе (переключатель в настройках активен только тогда).
  bool get supported => p == Plat.linux || p == Plat.windows;

  static const _common = 'При обрыве VPN трафик блокируется, а не идёт напрямую.';

  /// Подзаголовок переключателя в настройках.
  String get summary => switch (p) {
        Plat.linux => '$_common Только в режимах «Всё через выход» и «Всё, кроме РФ».',
        Plat.windows => '$_common Только в режимах «Всё через выход» и «Всё, кроме РФ»; локальная сеть доступна.',
        Plat.android => 'Трафик без VPN блокируется. Включается в системе: «Постоянный VPN».',
        Plat.other => '$_common Пока не реализовано.',
      };

  /// Как снять блокировку, если она мешает (null — снимать нечего).
  String? get howToRelease => switch (p) {
        Plat.linux => 'Кнопка «Снять блокировку» или команда brodd --release-killswitch. Совсем выключить — переключателем выше.',
        Plat.windows => 'Кнопка «Снять блокировку» или в консоли администратора brodctl killswitch release. Совсем выключить — переключателем выше.',
        _ => null,
      };

  /// Строка с ручными командами на экране «Демон остановился, трафик заблокирован» (null — на платформе экрана нет).
  String? get manualCommands => switch (p) {
        Plat.linux => 'Вручную: brodd --release-killswitch  или  sudo nft delete table inet brod_ks',
        Plat.windows => 'Вручную (консоль администратора): brodctl killswitch release',
        _ => null,
      };

  /// Пояснение на экране блокировки после падения демона.
  String get lockedExplanation =>
      'Пока VPN не включён снова, трафик блокируется. Блокировку можно снять: VPN останется выключенным.';
}
