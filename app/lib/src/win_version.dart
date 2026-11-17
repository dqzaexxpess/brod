/// Номер сборки Windows из `Platform.operatingSystemVersion` («"Windows 11 Pro" 10.0 (Build 22000)»); null — не разобрать.
int? windowsBuild(String osVersion) {
  final m = RegExp(r'Build (\d+)').firstMatch(osVersion);
  return m == null ? null : int.tryParse(m.group(1)!);
}

/// Предупреждение для Windows, у которых закончилась поддержка: Windows 11 21H2 (22000) и Windows 10 до 22H2 (19045). API демона на Windows
/// работает через сокеты AF_UNIX (драйвер `afunix.sys`), а на такой сборке при активной работе с ними был зафиксирован синий экран
/// (DRIVER_IRQL_NOT_LESS_OR_EQUAL, стек в `afunix.sys`; связь с Brod не доказана). Null — предупреждать нечего.
String? oldWindowsWarning(String osVersion) {
  final b = windowsBuild(osVersion);
  if (b == null) return null;
  final old = b < 19045 || (b >= 22000 && b < 22621);
  if (!old) return null;
  return 'Эта сборка Windows (Build $b) больше не получает обновлений безопасности. На ней замечен сбой системного драйвера сокетов '
      '(afunix.sys), через который приложение общается с демоном. Рекомендуем обновить Windows.';
}
