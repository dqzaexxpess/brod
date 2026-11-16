/// Режим запуска приложения: обычный, установка (`Brod.exe --setup`, запускает загрузчик setup.exe из распакованного архива)
/// или удаление (`Brod.exe --uninstall`, строка в «Параметры → Приложения»).
enum SetupMode { none, install, uninstall }

class SetupArgs {
  const SetupArgs(this.mode, {this.installDir = '', this.fromTemp = false});

  final SetupMode mode;
  final String installDir; // каталог установки; пусто — %ProgramFiles%\Brod (установка) или каталог, где лежит запущенный Brod.exe (удаление)
  final bool fromTemp; // удаление уже запущено из временной копии: каталог установки можно стирать

  bool get isSetup => mode != SetupMode.none;
}

SetupArgs parseSetupArgs(List<String> args) {
  var mode = SetupMode.none;
  var dir = '';
  var fromTemp = false;
  for (var i = 0; i < args.length; i++) {
    switch (args[i]) {
      case '--setup':
        mode = SetupMode.install;
      case '--uninstall':
        mode = SetupMode.uninstall;
      case '--from-temp':
        fromTemp = true;
      case '--install-dir':
        if (i + 1 < args.length) dir = args[++i];
    }
  }
  return SetupArgs(mode, installDir: dir, fromTemp: fromTemp);
}
