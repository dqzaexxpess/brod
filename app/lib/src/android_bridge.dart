import 'package:flutter/services.dart';

/// Запускаемое приложение для выбора «VPN только для…».
class AppInfo {
  const AppInfo(this.package, this.label);

  final String package;
  final String label;
}

/// Мост к Kotlin-слою Android (канал dev.godbtw.brod/core): ядро brodd встроено в процесс, VPN даёт VpnService, остальное — системные окна.
class AndroidCore {
  static const _ch = MethodChannel('dev.godbtw.brod/core');

  /// Запускает встроенное ядро и возвращает путь unix-сокета API.
  static Future<String> start() async => (await _ch.invokeMethod<String>('start'))!;

  /// Запрашивает у системы разрешение на VPN (окно «Запрос на подключение»); true — выдано.
  static Future<bool> prepareVpn() async => await _ch.invokeMethod<bool>('prepareVpn') ?? false;

  /// Запускаемые приложения (по названию).
  static Future<List<AppInfo>> listApps() async {
    final r = await _ch.invokeListMethod<Map<dynamic, dynamic>>('listApps') ?? const [];
    return [for (final m in r) AppInfo(m['package'] as String, m['label'] as String)];
  }

  /// Открывает ссылку системным обработчиком (tg://… — Telegram); false — подходящего приложения нет.
  static Future<bool> openUrl(String url) async => await _ch.invokeMethod<bool>('openUrl', {'url': url}) ?? false;

  /// Системные настройки VPN: «Постоянный VPN» и «Блокировать подключения без VPN» (киллсвитч на Android).
  static Future<bool> openVpnSettings() async => await _ch.invokeMethod<bool>('openVpnSettings') ?? false;

  /// Есть ли разрешение на точное местоположение «всегда» (фон нужен службе VPN): без него Android не отдаёт имя сети Wi-Fi (условия правил wifi_ssid, wifi_bssid).
  static Future<bool> locationGranted() async => await _ch.invokeMethod<bool>('locationGranted') ?? false;

  /// Системное окно запроса разрешения на местоположение; true — выдано.
  static Future<bool> requestLocation() async => await _ch.invokeMethod<bool>('requestLocation') ?? false;

  static Future<bool> batteryIgnored() async => await _ch.invokeMethod<bool>('batteryIgnored') ?? false;
  static Future<bool> batteryNoteHidden() async => await _ch.invokeMethod<bool>('batteryNoteHidden') ?? false;
  static Future<void> hideBatteryNote() => _ch.invokeMethod<bool>('hideBatteryNote');

  /// Просит исключить приложение из оптимизации батареи; возвращает итоговое состояние.
  static Future<bool> requestIgnoreBattery() async => await _ch.invokeMethod<bool>('requestIgnoreBattery') ?? false;

  /// Сохраняет файл через системный выбор места; возвращает адрес или null, если отменили.
  static Future<String?> saveFile(String name, String content) => _ch.invokeMethod<String>('saveFile', {'name': name, 'content': content});

  /// Передаёт скачанный и проверенный APK системному установщику. 'started' — окно установки открыто, 'permission' — сначала нужно
  /// разрешить Brod устанавливать приложения (открыта страница настроек; после разрешения нажать «Установить» ещё раз).
  static Future<String> installApk(String path) async => await _ch.invokeMethod<String>('installApk', {'path': path}) ?? 'started';

  /// Читает текстовый файл через системный выбор; null — отменили.
  static Future<String?> openFile() => _ch.invokeMethod<String>('openFile');

  /// «Поделиться журналом»: хвосты core.log и engine.log.
  static Future<void> shareLogs() async => _ch.invokeMethod<void>('shareLogs');

  /// Сведения об устройстве и сети (модель, версия системы, оболочка, батарея, DNS сети) для диагностического отчёта.
  static Future<String> deviceInfo() async => (await _ch.invokeMethod<String>('deviceInfo')) ?? '';

  /// Диагностический отчёт одним файлом (устройство, результаты тестов, хвосты журналов, JSON) в окно «Поделиться».
  static Future<void> shareReport(String text, String json) async => _ch.invokeMethod<void>('shareReport', {'text': text, 'json': json});

  /// Системное окно «Поделиться» с текстом (ссылка на узел и т. п.).
  static Future<void> shareText(String title, String text) async => _ch.invokeMethod<void>('shareText', {'title': title, 'text': text});
}
