import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import 'android_bridge.dart';
import 'api.dart';
import 'conn_rule.dart';
import 'daemon.dart';
import 'daemon_errors.dart';
import 'opener.dart';
import 'platform_paths.dart';
import 'private_file.dart';
import 'timing.dart';

enum Phase { starting, ready, failed }

/// Порядок списка узлов.
enum NodeSort { fresh, favorites, ping, speed, country, clean, proto, server }

const nodeSortNames = <NodeSort, String>{
  NodeSort.fresh: 'Свежие проверки',
  NodeSort.favorites: 'Избранные сначала',
  NodeSort.ping: 'По пингу',
  NodeSort.speed: 'По скорости',
  NodeSort.country: 'По стране',
  NodeSort.clean: 'По чистоте IP',
  NodeSort.proto: 'По протоколу',
  NodeSort.server: 'По адресу',
};

/// Период главного опроса на экране (2 с давали 60 запросов в минуту, два чтения базы демоном каждый). После своих команд
/// приложение обновляет состояние сразу, так что 5 с видны только для изменений снаружи (CLI, плановое переключение).
const Duration defaultPollEvery = Duration(seconds: 5);

/// Состояние приложения: опрашивает демон, выполняет команды, знает «подключаюсь…».
class AppModel extends ChangeNotifier {
  AppModel(this.api, {DaemonRunner? runner, this.pollEvery = defaultPollEvery, this.exitWaitSeconds = 45, LinkOpener? opener})
      : runner = runner ?? DaemonRunner(api),
        opener = opener ?? openLink;

  final DaemonApi api;
  final DaemonRunner runner;
  /// Перед включением VPN (Android: запрос разрешения у системы). Исключение ApiException с текстом отменяет включение.
  Future<void> Function()? beforeVpnOn;
  final LinkOpener opener; // открытие tg://-ссылок системным обработчиком (в тестах подменяется)
  final Duration pollEvery;
  /// Опрос ядра, пока приложение не на экране (Android: экран погашен или приложение свёрнуто): реже, чтобы не будить процессор впустую.
  /// Период опроса вне экрана; на компьютере (окно в трее) короче: по опросу обновляются значок и меню трея.
  Duration backgroundPoll = const Duration(seconds: 15);
  bool _foreground = true;
  bool get foreground => _foreground;
  Duration get currentPoll => _foreground ? pollEvery : backgroundPoll;

  /// Приложение на экране или нет. В фоне главный опрос замедляется, а опросы страниц и монитора молчат; при возврате опрос идёт сразу.
  void setForeground(bool v) {
    if (v == _foreground) return;
    _foreground = v;
    if (_timer != null && !_disposed) {
      _timer!.cancel();
      _timer = Timer.periodic(currentPoll, (_) => refresh());
      if (v) refresh();
    }
  }

  final int exitWaitSeconds; // сколько секунд после включения ждать рабочего выхода

  Phase phase = Phase.starting;
  String? fatal; // почему демон недоступен
  VpnState? vpn;
  DaemonStatus? status;
  ProbeResult? probe;
  String? notice; // последняя ошибка команды
  bool busy = false; // идёт переключение
  bool connecting = false; // переключили, ждём рабочего выхода
  String busyKind = ''; // что делаем: on | off | mode
  bool _abort = false;
  String? _pendingMode;
  int _modeSeq = 0;

  // список узлов
  List<NodeDto> nodes = const [];
  bool nodesLoading = false;
  String? nodesError;
  bool aliveOnly = true;
  bool favOnly = false;
  bool youtubeOnly = false;
  String search = '';
  String? countryFilter; // ISO-код выбранной страны
  NodeSort sort = NodeSort.fresh;
  // выбор узлов для пакетного замера
  bool selecting = false;
  Set<String> selected = {};
  MeasureDto measure = const MeasureDto();
  Timer? _measureTimer;
  String? nodeBusy; // id узла, с которым сейчас работаем
  // правила маршрутизации (черновик редактируется, «Применить» сохраняет и перезапускает VPN)
  List<RouteRuleDto> rules = [];
  List<String> ruleSets = const [];
  List<UserSetDto> userSets = const []; // свои списки по URL (в правилах выбираются вместе со встроенными)
  bool userSetsBusy = false;
  List<CountryDto> routeCountries = const []; // страны с проверенными узлами: выбор выхода правила
  Map<String, ViaStateDto> viaStates = const {}; // текущие привязки выходов правил
  List<RouteProfileDto> profiles = const []; // профили маршрутизации; пусто, пока ядро не ответило (старое ядро профилей не знает)
  RouteProfileDto? get activeProfile {
    for (final p in profiles) {
      if (p.active) return p;
    }
    return null;
  }
  bool rulesDirty = false;
  bool rulesLoading = false;

  // монитор: трафик, соединения, журнал
  TrafficStats traffic = const TrafficStats();
  List<ConnDto> conns = const [];
  bool connsClosed = false;
  String connSearch = '';
  List<String> engineLines = const [];
  List<EventDto> eventList = const [];
  String monitorTab = 'traffic'; // traffic | conns | log
  bool monitorOn = false;
  Timer? _monTimer;
  int _monTick = 0;

  // диагностика, статистика
  DiagDto? diag;
  bool diagRunning = false;
  List<DayTrafficDto> daily = const [];
  bool bestBusy = false;

  // свои серверы
  List<NodeDto> ownNodes = const [];
  bool ownLoading = false;

  // настройки
  List<SourceDto> sources = const [];
  Map<String, String> settings = const {};
  bool settingsLoading = false;
  String? _toastText; // короткое сообщение о результате действия; показывает один раз общий показыватель в оболочке (Shell) и очищает
  bool toastError = false;
  String? get toast => _toastText;
  set toast(String? v) {
    _toastText = v;
    toastError = false;
  }

  /// Сообщение об ошибке (в уведомлении — красным значком и дольше на экране).
  void _fail(String msg) {
    _toastText = msg;
    toastError = true;
  }

  /// Забирает ожидающее сообщение (и очищает его): каждое событие показывается ровно один раз, на какой бы странице ни был пользователь.
  (String, bool)? takeToast() {
    final t = _toastText;
    if (t == null) return null;
    final e = toastError;
    _toastText = null;
    toastError = false;
    return (t, e);
  }

  Timer? _timer;
  DateTime _lastReprobe = DateTime.fromMillisecondsSinceEpoch(0);
  bool _polling = false;
  bool _disposed = false;

  Future<void> start() async {
    phase = Phase.starting;
    fatal = null;
    _notify();
    final err = await runner.ensureRunning();
    if (err != null) {
      phase = Phase.failed;
      fatal = err;
      _notify();
      return;
    }
    phase = Phase.ready;
    await refresh();
    _timer?.cancel(); // повторный запуск («Повторить», снятие блокировки) не должен плодить опросы
    _timer = Timer.periodic(currentPoll, (_) => refresh());
    loadNodes(); // список узлов готов к первому открытию вкладки
  }

  Future<void> refresh() async {
    if (_polling || _disposed) return;
    _polling = true;
    var changed = false;
    try {
      final st = await api.status();
      final v = st.vpn ?? await api.vpn(); // новый демон отдаёт состояние VPN вместе со статусом, старый — отдельным запросом
      changed = v != vpn || st != status;
      vpn = v;
      status = st;
      if (phase != Phase.ready) {
        phase = Phase.ready;
        fatal = null;
        changed = true;
      }
    } catch (e) {
      if (!busy) {
        phase = Phase.failed;
        fatal = daemonDownText(e, logTail: runner.logTail());
        changed = true;
        if (daemonNotRunning(e)) unawaited(_restartDaemon());
      }
    } finally {
      _polling = false;
      if (changed) _notify(); // опрос каждые пару секунд не должен перерисовывать окно, пока ничего не изменилось
    }
    _reprobeIfFailed();
  }

  DateTime _lastRestart = DateTime.fromMillisecondsSinceEpoch(0);
  bool _restarting = false;

  /// Демон умер (падение, отключение питания) — на Windows его поднимает задача планировщика; пробуем не чаще раза в 20 с.
  Future<void> _restartDaemon() async {
    if (_restarting || _disposed || DateTime.now().difference(_lastRestart) < const Duration(seconds: 20)) return;
    _restarting = true;
    _lastRestart = DateTime.now();
    try {
      await runner.restartService(); // следующий опрос увидит демона и вернёт окно в рабочее состояние
    } finally {
      _restarting = false;
    }
  }

  /// Признак «правила киллсвитча стоят» (файл в каталоге рантайма; пишет демон). При остановленном демоне он значит: трафик заблокирован
  /// намеренно, а не сломана сеть.
  bool get killswitchHeld => runner.killswitchMarkerExists();

  /// Снять блокировку после падения демона и запустить демон заново (VPN останется выключенным: включить — самому).
  Future<void> releaseKillswitch() async {
    final err = await runner.releaseKillswitch();
    if (err != null) {
      fatal = err;
      _notify();
      return;
    }
    await start();
  }

  /// Включить/выключить VPN. После включения ждёт рабочего выхода (до 45 с) — как «подключаюсь…» в Throne.
  /// Повторное нажатие во время подключения прерывает его: ожидание останавливается и VPN выключается.
  Future<void> setOn(bool on) async {
    if (busy) {
      if (busyKind == 'on') cancelConnect();
      return;
    }
    await _switch(on: on);
  }

  /// Прервать подключение: если идёт ожидание выхода — оно останавливается сразу, если ещё перезапускается ядро — выключим, как только оно ответит.
  void cancelConnect() {
    if (!busy || busyKind != 'on') return;
    _abort = true;
    _notify();
  }

  /// Выбрать режим (запоминается; при включённом VPN применяется сразу). При выключенном VPN — мгновенно и без «подключения»;
  /// частые нажатия склеиваются: применяется последний выбор.
  Future<void> setMode(String mode) async {
    final cur = vpn;
    if (cur == null) return;
    final optimistic = cur.withMode(mode);
    if (busy) {
      _pendingMode = mode;
      vpn = optimistic;
      _notify();
      return;
    }
    if (!cur.on) {
      vpn = optimistic;
      _notify();
      try {
        final seq = ++_modeSeq;
        final v = await api.setVpn(mode: mode);
        if (seq == _modeSeq) vpn = v; // более новый выбор уже ушёл — старый ответ не затирает его
      } on ApiException catch (e) {
        notice = e.message;
      } catch (e) {
        notice = '$e';
      }
      _notify();
      return;
    }
    await _switch(mode: mode);
  }

  Future<void> _switch({bool? on, String? mode}) async {
    final sw = Stopwatch()..start();
    var tSet = Duration.zero, tWait = Duration.zero;
    busy = true;
    busyKind = on == true ? 'on' : (on == false ? 'off' : 'mode');
    _abort = false;
    notice = null;
    _notify();
    try {
      if (on == true && beforeVpnOn != null) await beforeVpnOn!();
      vpn = await api.setVpn(on: on, mode: mode);
      tSet = sw.elapsed;
      if (_abort) {
        vpn = await api.setVpn(on: false);
        toast = 'Подключение прервано';
      } else if (vpn!.on) {
        await _waitExit();
        tWait = sw.elapsed - tSet;
        if (_abort) {
          vpn = await api.setVpn(on: false);
          toast = 'Подключение прервано';
        }
      }
    } on ApiException catch (e) {
      notice = e.message;
    } catch (e) {
      notice = '$e';
    } finally {
      logTiming('переключатель ($busyKind)', {'запрос к демону': tSet == Duration.zero ? sw.elapsed : tSet, 'ожидание рабочего выхода': tWait});
      busy = false;
      connecting = false;
      busyKind = '';
      _abort = false;
      _notify();
    }
    final pending = _pendingMode;
    _pendingMode = null;
    if (pending != null && pending != vpn?.mode && !_disposed) {
      await setMode(pending); // пока шла смена, пользователь выбрал другой режим
      return;
    }
    await refresh();
  }

  Future<void> _waitExit() async {
    connecting = true;
    _notify();
    final sw = Stopwatch()..start();
    while (sw.elapsed.inSeconds < exitWaitSeconds && !_disposed && !_abort) {
      try {
        probe = await api.probe(quick: true); // лёгкая проверка: «подключено» показывается, как только выход пропускает данные
        if (probe!.ok) {
          unawaited(checkExit()); // строгую проверку (TLS) делаем следом, не задерживая показ; при провале значок станет «проблема»
          return;
        }
      } catch (_) {}
      for (var k = 0; k < 5 && !_abort; k++) {
        await Future<void>.delayed(const Duration(milliseconds: 100)); // дробный сон: отмена срабатывает за 0,1 с
      }
    }
  }

  /// Если VPN включён, а последняя проверка выхода провалилась, перепроверяем раз в 15 с: иконка «проблема» должна сама погаснуть, когда выход вернулся.
  void _reprobeIfFailed() {
    if (busy || _disposed || vpn?.on != true || probe == null || probe!.ok) return;
    final now = DateTime.now();
    if (now.difference(_lastReprobe) < const Duration(seconds: 15)) return;
    _lastReprobe = now;
    checkExit();
  }

  /// Ручная проверка выхода.
  Future<void> checkExit() async {
    try {
      probe = await api.probe();
    } on ApiException catch (e) {
      probe = ProbeResult(ok: false, detail: e.message.contains('активного узла нет') ? 'узел не выбран: выберите его на вкладке «Узлы»' : e.message);
    } catch (e) {
      probe = ProbeResult(ok: false, detail: '$e');
    }
    _notify();
  }

  // ---- обновление приложения ----

  UpdateDto? updateInfo; // null — ещё не запрашивали
  bool updateBusy = false; // идёт проверка
  Timer? _updTimer;

  Future<void> loadUpdate() async {
    try {
      updateInfo = await api.update();
      _pollUpdate();
      _notify();
    } catch (_) {} // старое ядро без обновлений: диалог покажет пустое состояние
  }

  Future<void> checkUpdate() async {
    updateBusy = true;
    toast = null;
    _notify();
    try {
      updateInfo = await api.checkUpdate();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    } finally {
      updateBusy = false;
      _notify();
    }
    _pollUpdate();
  }

  Future<void> downloadUpdate({bool allowMetered = false}) async {
    try {
      updateInfo = await api.downloadUpdate(allowMetered: allowMetered);
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    _notify();
    _pollUpdate();
  }

  /// Установка из приложения доступна на этой платформе; передача APK системному установщику (подменяется в тестах).
  bool installSupported = Platform.isAndroid || Platform.isLinux || Platform.isWindows;
  bool installViaSystem = Platform.isAndroid; // установку делает система (Android), а не помощник ядра
  Future<void> Function()? onQuitForInstall; // закрыть приложение после запуска установки (main.dart: выход из трея)
  Future<String> Function(String path) installApk = AndroidCore.installApk;
  String updateNote = ''; // что сказать после нажатия «Установить»
  bool updateInstalling = false;

  /// «Установить»: Android передаёт проверенный APK системному установщику (подтверждает пользователь); Linux и Windows — ядро проверяет и
  /// распаковывает релиз, запускает помощника и завершается, приложение закрывается, помощник ставит новое и запускает приложение снова.
  Future<void> installUpdate() async {
    final path = updateInfo?.download.path ?? '';
    if (path.isEmpty || updateInstalling) return;
    updateInstalling = true;
    updateNote = '';
    _notify();
    try {
      if (!installViaSystem) {
        updateInfo = await api.installUpdate(appPid: pid);
        updateNote = 'Устанавливаю: приложение закроется и откроется заново.';
        _notify();
        if (Platform.isWindows) await _relaunchWindows();
        await Future<void>.delayed(const Duration(seconds: 1));
        await onQuitForInstall?.call();
        return;
      }
      final r = await installApk(path);
      updateNote = r == 'permission'
          ? 'Разрешите Brod устанавливать приложения, вернитесь и снова нажмите «Установить».'
          : 'Окно установки открыто: подтвердите обновление. Приложение перезапустится само.';
    } catch (e) {
      updateNote = 'Установка не удалась: ${e is PlatformException ? (e.message ?? e.code) : e}';
    } finally {
      updateInstalling = false;
      _notify();
    }
  }

  // Windows: демон-служба не может запустить окно в сеансе пользователя, поэтому приложение перед выходом оставляет скрытый PowerShell: он ждёт
  // итог установки (файл last-install.json новее, чем несколько секунд назад) или 5 минут и запускает Brod.exe.
  // Запуск через `cmd /c start`: PowerShell, запущенный напрямую в режиме detached (DETACHED_PROCESS, без консоли), завершается сразу
  // и ничего не выполняет (проверено на Windows 11); start даёт ему свою консоль, которую он сразу прячет. Скрипт — в -EncodedCommand,
  // чтобы cmd не разбирал спецсимволы пути.
  Future<void> _relaunchWindows() async {
    final result = '${stateDirPath(Platform.environment, windows: true)}\\updates\\last-install.json'.replaceAll("'", "''");
    final exe = Platform.resolvedExecutable.replaceAll("'", "''");
    final script = "\$r='$result'; for (\$i=0; \$i -lt 300; \$i++) { if (Test-Path \$r) { try { \$j = Get-Content \$r -Raw | ConvertFrom-Json; "
        "if ([datetimeoffset]::Parse(\$j.at) -gt [datetimeoffset]::UtcNow.AddSeconds(-20)) { break } } catch {} }; Start-Sleep -Seconds 1 }; Start-Process '$exe'";
    final utf16 = <int>[for (final u in script.codeUnits) ...[u & 0xff, u >> 8]];
    await Process.start('cmd.exe', ['/c', 'start', '""', '/min', 'powershell.exe', '-NoProfile', '-WindowStyle', 'Hidden', '-EncodedCommand', base64.encode(utf16)],
        mode: ProcessStartMode.detached);
  }

  /// Пока файл скачивается, раз в секунду перечитывает ход (в фоне приложения не опрашивает).
  void _pollUpdate() {
    _updTimer?.cancel();
    if (_disposed || updateInfo?.download.downloading != true) return;
    _updTimer = Timer.periodic(const Duration(seconds: 1), (t) async {
      if (_disposed || !_foreground) return;
      try {
        updateInfo = await api.update();
        _notify();
      } catch (_) {}
      if (updateInfo?.download.downloading != true) t.cancel();
    });
  }

  // ---- правила маршрутизации ----

  Future<void> loadRules() async {
    rulesLoading = true;
    _notify();
    try {
      final r = await api.routeRules();
      rules = r.rules;
      ruleSets = r.sets;
      try {
        userSets = await api.userSets();
      } catch (_) {} // старое ядро без своих списков
      routeCountries = r.countries;
      viaStates = r.via;
      rulesDirty = false;
      try {
        profiles = await api.routeProfiles();
      } catch (_) {
        profiles = const []; // ядро без профилей: меню профилей не показывается
      }
    } catch (e) {
      _fail('Правила не загрузились: $e');
    } finally {
      rulesLoading = false;
      _notify();
    }
  }

  /// Операция с профилем с последующей перезагрузкой правил; op возвращает текст ошибки перезапуска VPN или null.
  Future<void> _profileOp(Future<String?> Function() op, String ok) async {
    rulesLoading = true;
    toast = null;
    _notify();
    try {
      final err = await op();
      toast = err == null || err.isEmpty ? ok : '$ok, но VPN не перезапустился: $err';
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    } finally {
      rulesLoading = false;
    }
    await loadRules();
  }

  Future<void> switchProfile(String id) => _profileOp(() => api.activateRouteProfile(id), 'Профиль переключён');
  Future<void> createProfile(String name, {String from = ''}) => _profileOp(() async {
        await api.createRouteProfile(name, from: from);
        return null;
      }, 'Профиль создан');
  Future<void> renameProfile(String id, String name) => _profileOp(() async {
        await api.renameRouteProfile(id, name);
        return null;
      }, 'Профиль переименован');
  Future<void> deleteProfile(String id) => _profileOp(() async {
        await api.deleteRouteProfile(id);
        return null;
      }, 'Профиль удалён');
  Future<void> importProfile(String text, {String name = ''}) => _profileOp(() async {
        await api.importRouteProfile(text, name: name);
        return null;
      }, 'Профиль добавлен, но не включён');

  /// Ссылка на профиль для передачи (null — не удалось, причина в уведомлении).
  Future<String?> shareProfile(String id) async {
    try {
      return await api.shareRouteProfile(id);
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    return null;
  }

  /// Правило «через выход» из журнала соединений: ставится первым, чтобы сработало раньше прежних; VPN перезапускается и применяет его.
  Future<bool> sendConnViaExit(ConnRuleOffer o) async {
    if (!rulesDirty) await loadRules(); // несохранённые правки страницы правил не затираем: правило добавляется к ним и сохраняется вместе
    if (rules.any((r) => r.title == o.title)) {
      toast = 'Такое правило уже есть';
      _notify();
      return true;
    }
    rules = [RouteRuleDto(title: o.title, action: 'proxy', match: o.match), ...rules];
    rulesDirty = true;
    return saveRules();
  }

  void addRule(RouteRuleDto r) {
    rules = [...rules, r];
    rulesDirty = true;
    _notify();
  }

  void replaceRule(int i, RouteRuleDto r) {
    rules = [...rules]..[i] = r;
    rulesDirty = true;
    _notify();
  }

  void removeRule(int i) {
    rules = [...rules]..removeAt(i);
    rulesDirty = true;
    _notify();
  }

  void moveRule(int from, int to) {
    final l = [...rules];
    final r = l.removeAt(from);
    l.insert(to > from ? to - 1 : to, r);
    rules = l;
    rulesDirty = true;
    _notify();
  }

  /// Сохраняет правила; включённый VPN перезапускается и применяет их. Возвращает true, если всё прошло.
  Future<bool> saveRules() async {
    rulesLoading = true;
    toast = null;
    _notify();
    try {
      final err = await api.putRouteRules(rules);
      if (err == null) {
        toast = 'Правила применены';
      } else {
        _fail('Правила сохранены, но VPN не перезапустился: $err');
      }
      rulesDirty = false;
      await loadRules();
      await refresh();
      return err == null;
    } on ApiException catch (e) {
      _fail(e.message);
      return false;
    } catch (e) {
      _fail('$e');
      return false;
    } finally {
      rulesLoading = false;
      _notify();
    }
  }

  // ---- настройки ----

  bool get viaWarp => settings['check.via_warp'] == '1';

  Future<void> loadSettings() async {
    settingsLoading = true;
    _notify();
    try {
      sources = await api.sources();
      settings = await api.settings();
    } catch (e) {
      _fail('Настройки не загрузились: $e');
    } finally {
      settingsLoading = false;
      _notify();
    }
  }

  Future<void> _act(Future<void> Function() f, {String? ok}) async {
    toast = null;
    try {
      await f();
      if (ok != null) toast = ok;
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    await loadSettings();
  }

  Future<void> setSourceEnabled(SourceDto s, bool on) => _act(() => api.setSourceEnabled(s.name, on));
  Future<void> addSource(String name, String url) => _act(() => api.addSource(name, url), ok: 'Источник добавлен');
  Future<void> removeSource(SourceDto s) => _act(() => api.removeSource(s.name), ok: 'Источник удалён');

  /// Найденные автопоиском источники, сгруппированные по содержимому (зеркала вместе). Ошибка загрузки (старый демон без маршрута) скрывает
  /// вход на экран «Найденные источники», а не пугает тостом.
  List<CandidateGroupDto> candidateGroups = const [];
  bool candidatesBusy = false;
  bool candidatesOk = false; // демон отдал группы (вход показывается); false — демон старый или недоступен

  Future<void> loadCandidates() async {
    try {
      candidateGroups = await api.candidateGroups();
      candidatesOk = true;
    } catch (_) {
      candidateGroups = const [];
      candidatesOk = false;
    }
    _notify();
  }

  /// Добавляет только указанного кандидата (представителя или члена группы).
  Future<void> addCandidate(CandidateDto c) async {
    await _act(() async {
      final name = await api.addCandidate(c.id);
      toast = 'Источник «$name» добавлен; узлы появятся после обновления подписок';
    });
    await loadCandidates();
  }

  /// Скрывает всю группу (и одиночного кандидата: это группа из одного).
  Future<void> dismissCandidateGroup(CandidateGroupDto g) async {
    await _act(() => api.dismissCandidateGroup(g.id));
    await loadCandidates();
  }

  /// Внеочередной проход автопоиска (синхронно, до нескольких минут).
  Future<void> scanCandidates() async {
    if (candidatesBusy) return;
    candidatesBusy = true;
    toast = null;
    _notify();
    try {
      toast = await api.scanCandidates();
    } on ApiException catch (e) {
      _fail(e.status == 409 ? scanConflictText(e.message) : e.message);
    } catch (e) {
      _fail('$e');
    }
    candidatesBusy = false;
    await loadCandidates();
  }

  /// 409 на проходе поиска: сеть платная без согласия либо проход уже идёт (тексты демона для обновления подписок, здесь они сбивают с толку).
  static String scanConflictText(String daemonMessage) => daemonMessage.contains('мобильн') || daemonMessage.contains('оплат')
      ? 'Поиск не запущен: сеть мобильная или с платным трафиком, а поиск скачивает десятки мегабайт. Подключитесь к Wi-Fi и повторите.'
      : 'Поиск уже идёт.';

  bool get killswitchOn => settings['killswitch.enabled'] != '0'; // по умолчанию включён
  bool get fakeipOn => settings['dns.fakeip'] == '1'; // «Быстрый DNS» (эксперимент), по умолчанию выключен
  String get degradeAction => settings['degrade.action'] ?? 'notify';
  String get notifyLevel => settings['notify.level'] ?? 'important';

  Future<void> setSetting(String key, String value) => _act(() => api.putSetting(key, value));

  /// Автоподключение при запуске демона: last | always | off (действует со следующего запуска, текущее состояние VPN не меняется).
  Future<void> setAutoconnect(String value) => _act(() async {
        vpn = await api.setVpn(autoconnect: value);
        _notify();
      });
  /// Раздельное туннелирование (Android): режим и список одной записью. Возвращает текст ошибки или null, если всё применено.
  Future<String?> setTunApps(String mode, List<String> apps) async {
    toast = null;
    String? err;
    try {
      final restart = await api.setTunApps(mode, apps);
      if (restart.isNotEmpty) {
        err = 'Сохранено, но VPN не перезапустился: $restart';
      } else {
        toast = 'Применено';
      }
    } on ApiException catch (e) {
      err = e.message;
    } catch (e) {
      err = 'Не сохранилось: $e';
    }
    if (err != null) _fail(err);
    await loadSettings();
    return err;
  }

  bool get otaAuto => settings['ota.sources'] != '0'; // по умолчанию включено
  Future<void> setOtaAuto(bool on) => _act(() => api.putSetting('ota.sources', on ? '1' : '0'));

  /// Забрать подписанный список источников у издателя сейчас (раз в сутки ядро делает это само).
  Future<void> updateOtaSources() async {
    toast = null;
    _notify();
    try {
      toast = await api.otaSources();
      await loadSettings();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    _notify();
  }

  Future<void> setViaWarp(bool on) => _act(() => api.putSetting('check.via_warp', on ? '1' : '0'));

  /// Идёт регистрация или загрузка аккаунта WARP (ядро при этом перезапускается).
  bool warpBusy = false;

  /// Окно «Cloudflare WARP»: состояние, ход и итог подбора профиля под сеть.
  WarpStateDto? warpState;
  List<WarpScanResultDto>? warpScanResults;
  bool warpScanning = false;

  Future<void> loadWarp() async {
    try {
      warpState = await api.warpState();
      _notify();
    } catch (_) {}
  }

  /// Подбирает профиль под текущую сеть и применяет лучший (ядро пересоздаётся); итог остаётся в warpScanResults.
  Future<void> scanWarp() async {
    if (warpScanning) return;
    warpScanning = true;
    warpScanResults = null;
    toast = null;
    _notify();
    try {
      warpScanResults = await api.warpScan(apply: true, stopAtGood: 0);
      final ok = warpScanResults!.where((r) => r.ok).length;
      toast = ok == 0 ? 'Подбор закончен: для этой сети ничего не нашлось' : 'Подбор закончен: рабочих вариантов $ok';
      await refresh();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    warpScanning = false;
    await loadWarp();
  }

  Future<void> applyWarpParams(Map<String, String> params) async {
    toast = null;
    try {
      await api.warpParams(params);
      toast = 'Параметры WARP применены';
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    await loadWarp();
  }

  Future<void> resetWarp() async {
    toast = null;
    try {
      await api.warpReset();
      toast = 'Параметры WARP сброшены';
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    await loadWarp();
  }

  Future<void> setWarpAuto(bool on) async {
    await _act(() => api.putSetting('warp.auto', on ? '1' : '0'));
    await loadWarp();
  }

  Future<void> warpRegister({bool replace = false}) => _warpChange(() => api.warpRegister(replace: replace));

  Future<void> warpImportFile(String path, {bool replace = false}) => _warpChange(() async {
        final doc = jsonDecode(await File(path).readAsString());
        if (doc is! Map<String, dynamic>) throw const FormatException('это не файл аккаунта WARP');
        return api.warpImport(doc, replace: replace);
      });

  Future<void> warpImportAndroid({bool replace = false}) => _warpChange(() async {
        final text = await AndroidCore.openFile();
        if (text == null) return ''; // выбор файла отменён
        final doc = jsonDecode(text);
        if (doc is! Map<String, dynamic>) throw const FormatException('это не файл аккаунта WARP');
        return api.warpImport(doc, replace: replace);
      });

  Future<void> _warpChange(Future<String> Function() f) async {
    warpBusy = true;
    toast = null;
    _notify();
    try {
      final r = await f();
      if (r.isNotEmpty) toast = r;
      await refresh(); // ядро пересоздано: статус и признак «WARP настроен» обновляются
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    warpBusy = false;
    _notify();
  }

  Future<void> loadUserSets() async {
    try {
      userSets = await api.userSets();
      _notify();
    } catch (_) {}
  }

  /// Добавляет список по адресу: ядро скачивает и проверяет его сразу; ошибка (не https, не rule-set, нет ответа) показывается тостом.
  Future<bool> addUserSet(String url, String title) async {
    userSetsBusy = true;
    toast = null;
    _notify();
    try {
      await api.addUserSet(url, title);
      userSets = await api.userSets();
      ruleSets = (await api.routeRules()).sets;
      return true;
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    } finally {
      userSetsBusy = false;
      _notify();
    }
    return false;
  }

  Future<void> removeUserSet(String id) async {
    await _act(() => api.removeUserSet(id));
    await loadUserSets();
  }

  Future<void> updateRuleSets() async {
    toast = null;
    _notify();
    try {
      toast = await api.updateRuleSets();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    _notify();
  }

  // ---- узлы ----

  /// Отфильтрованный и отсортированный список для показа.
  List<NodeDto> get visibleNodes {
    final q = search.trim().toLowerCase();
    var l = nodes.where((n) {
      if (youtubeOnly && !n.youtube) return false;
      if (favOnly && !n.favorite) return false;
      if (countryFilter != null && n.exit?.country != countryFilter) return false;
      if (q.isNotEmpty && !('${n.server} ${n.proto} ${n.port}'.toLowerCase().contains(q))) return false;
      return true;
    }).toList();
    int byFresh(NodeDto a, NodeDto b) => (b.l2At ?? DateTime.fromMillisecondsSinceEpoch(0)).compareTo(a.l2At ?? DateTime.fromMillisecondsSinceEpoch(0));
    switch (sort) {
      case NodeSort.fresh:
        l.sort(byFresh);
      case NodeSort.favorites:
        l.sort((a, b) => a.favorite == b.favorite ? byFresh(a, b) : (a.favorite ? -1 : 1));
      case NodeSort.ping:
        l.sort((a, b) {
          // без данных — в конец
          final pa = a.latencyMs > 0 ? a.latencyMs : 1 << 30, pb = b.latencyMs > 0 ? b.latencyMs : 1 << 30;
          final c = pa.compareTo(pb);
          return c != 0 ? c : byFresh(a, b);
        });
      case NodeSort.speed:
        l.sort((a, b) {
          final c = b.speedKBps.compareTo(a.speedKBps); // быстрые первыми, без замера — в конец
          return c != 0 ? c : byFresh(a, b);
        });
      case NodeSort.country:
        l.sort((a, b) {
          final ca = a.exit?.country ?? '~', cb = b.exit?.country ?? '~'; // без данных — в конец
          final c = ca.compareTo(cb);
          return c != 0 ? c : byFresh(a, b);
        });
      case NodeSort.clean:
        l.sort((a, b) {
          final c = _cleanRank(a).compareTo(_cleanRank(b));
          return c != 0 ? c : byFresh(a, b);
        });
      case NodeSort.proto:
        l.sort((a, b) {
          final c = a.proto.compareTo(b.proto);
          return c != 0 ? c : byFresh(a, b);
        });
      case NodeSort.server:
        l.sort((a, b) => a.server.compareTo(b.server));
    }
    return l;
  }

  static int _cleanRank(NodeDto n) => switch (n.exit?.clean) { 'clean' => 0, 'mixed' => 1, 'dirty' => 2, _ => 3 };

  /// Страны, которые есть в списке (для фильтра), с числом узлов; по убыванию.
  List<MapEntry<String, int>> get countries {
    final m = <String, int>{};
    for (final n in nodes) {
      final c = n.exit?.country;
      if (c != null && c.isNotEmpty) m[c] = (m[c] ?? 0) + 1;
    }
    return m.entries.toList()..sort((a, b) => b.value != a.value ? b.value.compareTo(a.value) : a.key.compareTo(b.key));
  }

  void setCountry(String? code) {
    countryFilter = code;
    _notify();
  }

  // ---- лучший узел, диагностика, статистика, копия ----

  /// Подключиться к лучшему живому узлу (чистота, YouTube, скорость, пинг; свои серверы — с бонусом).
  Future<void> connectBest() async {
    if (bestBusy) return;
    bestBusy = true;
    toast = null;
    _notify();
    try {
      final n = await api.connectBest();
      toast = 'Выбран лучший узел: ${n.title}';
      probe = null;
      await refresh();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    } finally {
      bestBusy = false;
      _notify();
    }
  }

  Future<void> runDiagnostics() async {
    diagRunning = true;
    _notify();
    try {
      diag = await api.diagnostics();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    } finally {
      diagRunning = false;
      _notify();
    }
  }

  Future<void> loadDaily() async {
    try {
      daily = await api.dailyTraffic(days: 14);
      _notify();
    } catch (_) {}
  }

  /// Поделиться узлом: на Android — системное окно «Поделиться», на остальных — ссылка в буфер обмена. Ссылка содержит секреты (uuid, пароль).
  Future<void> shareNode(NodeDto n) async {
    try {
      final link = await api.nodeLink(n.id);
      if (Platform.isAndroid) {
        await AndroidCore.shareText('Brod: ${n.title}', link);
      } else {
        await Clipboard.setData(ClipboardData(text: link));
        toast = 'Ссылка скопирована. В ней пароль или ключ.';
      }
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('Не удалось поделиться: $e');
    }
    _notify();
  }

  /// Android: копия через системный выбор места сохранения.
  Future<void> saveBackupAndroid() async {
    try {
      final doc = await api.backupExport();
      final n = DateTime.now();
      String two(int v) => v.toString().padLeft(2, '0');
      final at = await AndroidCore.saveFile('brod-backup-${n.year}-${two(n.month)}-${two(n.day)}.json', const JsonEncoder.withIndent('  ').convert(doc));
      if (at != null) toast = 'Копия сохранена';
    } catch (e) {
      _fail('Не удалось сохранить копию: $e');
    }
    _notify();
  }

  /// Что копия изменит: настройки по ключам, правила, источники, свои серверы. Копия из чужих рук может
  /// выключить киллсвитч, увести трафик правилами и подключить сторонний источник, поэтому всё это показывается до применения.
  static String backupSummary(Map<String, dynamic> doc) {
    final b = StringBuffer();
    final settings = (doc['settings'] as Map?)?.cast<String, dynamic>() ?? const {};
    if (settings.isNotEmpty) {
      b.writeln('Настройки (${settings.length}):');
      for (final e in settings.entries.take(20)) {
        b.writeln('  ${e.key} = ${e.value}');
      }
      if (settings.length > 20) b.writeln('  … и ещё ${settings.length - 20}');
    }
    final rules = (doc['rules'] as List?)?.length;
    if (rules != null) b.writeln('Правила маршрутизации: заменятся, будет $rules.');
    final sources = (doc['sources'] as List?) ?? const [];
    if (sources.isNotEmpty) {
      b.writeln('Источники подписок (${sources.length}):');
      for (final x in sources.take(8)) {
        if (x is Map) b.writeln('  ${x['name']}: ${x['url']}');
      }
      if (sources.length > 8) b.writeln('  … и ещё ${sources.length - 8}');
    }
    final own = (doc['own'] as List?) ?? const [];
    if (own.isNotEmpty) {
      b.writeln('Свои серверы (${own.length}), закрепятся и попадут в проверки:');
      for (final x in own.take(8)) {
        if (x is Map) b.writeln('  ${x['server']}:${x['port']}');
      }
      if (own.length > 8) b.writeln('  … и ещё ${own.length - 8}');
    }
    if (b.isEmpty) b.writeln('В копии нет ни настроек, ни правил, ни серверов.');
    return b.toString().trimRight();
  }

  /// Android: восстановление из выбранного системным окном файла.
  Future<bool> restoreBackupAndroid({Future<bool> Function(String summary)? confirm}) async {
    try {
      final text = await AndroidCore.openFile();
      if (text == null) return false;
      final doc = jsonDecode(text);
      if (doc is! Map<String, dynamic>) throw const FormatException('это не копия Brod');
      if (confirm != null && !await confirm(backupSummary(doc))) return false;
      toast = await api.backupImport(doc);
      await Future.wait([loadSettings(), loadNodes(), loadOwn(), loadRules()]);
      _notify();
      return true;
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('Не удалось восстановить: $e');
    }
    _notify();
    return false;
  }

  /// Сохранить копию в файл; возвращает путь или null при ошибке.
  Future<String?> saveBackup(String path) async {
    try {
      final doc = await api.backupExport();
      await writePrivateFile(path, const JsonEncoder.withIndent('  ').convert(doc)); // в копии пароли и uuid серверов
      toast = 'Копия сохранена: $path';
      _notify();
      return path;
    } catch (e) {
      _fail('Не удалось сохранить копию: $e');
      _notify();
      return null;
    }
  }

  Future<bool> restoreBackup(String path, {Future<bool> Function(String summary)? confirm}) async {
    try {
      final doc = jsonDecode(await File(path).readAsString());
      if (doc is! Map<String, dynamic>) throw const FormatException('это не копия Brod');
      if (confirm != null && !await confirm(backupSummary(doc))) return false;
      toast = await api.backupImport(doc);
      await Future.wait([loadSettings(), loadNodes(), loadOwn(), loadRules()]);
      _notify();
      return true;
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('Не удалось восстановить: $e');
    }
    _notify();
    return false;
  }

  /// Название любого узла.
  Future<void> setNodeLabel(NodeDto n, String label) async {
    try {
      await api.setLabel(n.id, label);
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    await loadNodes();
    await loadOwn();
  }

  /// Правка своего сервера; link — замена ссылкой. Возвращает true при успехе.
  Future<bool> editOwn(NodeDto n, {String? label, String? server, int? port, String? link}) async {
    toast = null;
    try {
      await api.editOwn(n.id, label: label, server: server, port: port, link: link);
      toast = 'Сервер изменён';
      await loadOwn();
      await loadNodes();
      return true;
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    _notify();
    return false;
  }

  // ---- монитор ----

  /// Окно «Монитор» открыто/закрыто: опрос идёт только пока оно на виду.
  void setMonitor({bool? on, String? tab}) {
    monitorTab = tab ?? monitorTab;
    if (on != null && on != monitorOn) {
      monitorOn = on;
      _monTimer?.cancel();
      if (on) {
        _monTick = 0;
        _monTimer = Timer.periodic(const Duration(seconds: 1), (_) => _monPoll());
        _monPoll();
      }
    } else if (monitorOn) {
      _monTick = 0;
      _monPoll();
    }
    _notify();
  }

  void setConnsView({bool? closed, String? search}) {
    connsClosed = closed ?? connsClosed;
    connSearch = search ?? connSearch;
    _notify();
    if (closed != null) _monPoll();
  }

  List<ConnDto> get visibleConns {
    final q = connSearch.trim().toLowerCase();
    return conns.where((c) {
      if (!connsClosed && !c.open) return false;
      if (q.isNotEmpty && !('${c.dest} ${c.process} ${c.node} ${c.route} ${c.routeName}'.toLowerCase().contains(q))) return false;
      return true;
    }).toList();
  }

  Future<void> _monPoll() async {
    if (_disposed || !_foreground) return;
    _monTick++;
    try {
      traffic = await api.traffic(n: 120);
      if (monitorTab == 'conns') conns = await api.connections(closed: connsClosed, limit: 400);
      if (monitorTab == 'log' && _monTick % 3 == 1) {
        engineLines = await api.engineLog(n: 400);
        eventList = await api.events(limit: 300);
      }
      _notify();
    } catch (_) {}
  }

  Future<void> closeConn(ConnDto c) async {
    try {
      await api.closeConnection(c.id);
    } catch (_) {}
    _monPoll();
  }

  // ---- свои серверы ----

  Future<void> loadOwn() async {
    ownLoading = true;
    _notify();
    try {
      ownNodes = await api.nodes(source: 'own', limit: 100);
    } catch (e) {
      _fail('Свои серверы не загрузились: $e');
    } finally {
      ownLoading = false;
      _notify();
    }
  }

  /// Добавить свои серверы из текста (ссылки по одной в строке или готовый конфиг). Возвращает true при успехе.
  Future<bool> addOwn(String text) async {
    toast = null;
    try {
      toast = await api.addOwn(text);
      await loadOwn();
      await loadNodes();
      return true;
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    _notify();
    return false;
  }

  Future<void> removeOwn(NodeDto n) async {
    toast = null;
    try {
      await api.removeOwn(n.id);
      toast = 'Сервер удалён';
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    await loadOwn();
    await loadNodes();
  }

  // ---- пакетные замеры ----

  void setSelecting(bool on) {
    selecting = on;
    if (!on) selected = {};
    _notify();
  }

  void toggleSelect(String id) {
    selected = {...selected};
    if (!selected.remove(id)) selected.add(id);
    _notify();
  }

  void selectAllVisible({int limit = 500}) {
    selected = visibleNodes.take(limit).map((n) => n.id).toSet();
    _notify();
  }

  void clearSelection() {
    selected = {};
    _notify();
  }

  /// Запустить пакетный замер выбранных (или переданных) узлов; ход опрашивается раз в секунду.
  Future<void> startMeasure({bool ping = false, bool exit = false, bool speed = false, List<String>? ids}) async {
    final list = ids ?? selected.toList();
    if (list.isEmpty) return;
    toast = null;
    try {
      measure = await api.startMeasure(list, ping: ping, exit: exit, speed: speed);
      _measureTimer?.cancel();
      _measureTimer = Timer.periodic(const Duration(seconds: 1), (_) {
        if (_foreground) _pollMeasure();
      });
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    _notify();
  }

  Future<void> _pollMeasure() async {
    try {
      final m = await api.measureState();
      final was = measure.running;
      measure = m;
      if (!m.running) {
        _measureTimer?.cancel();
        if (was) {
          toast = m.canceled ? 'Замер остановлен' : 'Замер завершён: удалось ${m.ok}';
          selecting = false;
          selected = {};
          await loadNodes();
        }
      }
      _notify();
    } catch (_) {}
  }

  Future<void> cancelMeasure() async {
    try {
      measure = await api.cancelMeasure();
    } catch (_) {}
    _notify();
  }

  /// Замерить скорость скачивания через узел сейчас.
  Future<void> measureSpeed(NodeDto n) async {
    nodeBusy = n.id;
    toast = null;
    _notify();
    try {
      final kb = await api.measureSpeed(n.id);
      toast = 'Скорость ${n.title}: ${(kb * 8 / 1024).toStringAsFixed(1)} Мбит/с';
      await loadNodes();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    } finally {
      nodeBusy = null;
      _notify();
    }
  }

  /// Определить страну и чистоту выхода узла сейчас.
  Future<void> detectExit(NodeDto n) async {
    nodeBusy = n.id;
    toast = null;
    _notify();
    try {
      final e = await api.exitInfo(n.id);
      toast = 'Выход ${n.title}: ${e.country} ${e.ip}';
      await loadNodes();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    } finally {
      nodeBusy = null;
      _notify();
    }
  }

  void setNodeView({bool? alive, bool? fav, bool? youtube, String? query, NodeSort? order}) {
    final reload = alive != null && alive != aliveOnly;
    aliveOnly = alive ?? aliveOnly;
    favOnly = fav ?? favOnly;
    youtubeOnly = youtube ?? youtubeOnly;
    search = query ?? search;
    sort = order ?? sort;
    _notify();
    if (reload) loadNodes();
  }

  static String _sig(List<NodeDto> l) => l.map((n) => n.signature).join(';');

  bool _quietNodesBusy = false;

  /// quiet — подгрузка списка во время долгой операции (проверка узлов): не трогает флаг «идёт загрузка» и не пересекается сама с собой.
  Future<void> loadNodes({bool quiet = false}) async {
    if (quiet) {
      if (_quietNodesBusy) return;
      _quietNodesBusy = true;
    } else if (nodesLoading) {
      return;
    }
    final first = nodes.isEmpty && !quiet;
    if (first) {
      nodesLoading = true;
      _notify();
    }
    var changed = first;
    try {
      final l = await api.nodes(alive: aliveOnly, limit: aliveOnly ? 400 : 1000);
      changed = changed || _sig(l) != _sig(nodes);
      nodes = l;
      if (nodesError != null) changed = true;
      nodesError = null;
    } catch (e) {
      nodesError = '$e';
      changed = true;
    } finally {
      if (quiet) {
        _quietNodesBusy = false;
      } else {
        nodesLoading = false;
      }
      if (changed) _notify();
    }
  }

  /// Сделать узел активным (липкий выбор: остаётся, пока сами не смените).
  Future<void> connectNode(NodeDto n) async {
    nodeBusy = n.id;
    toast = null;
    _notify();
    try {
      await api.connectNode(n.id);
      toast = 'Активный выход: ${n.proto} ${n.title}';
      probe = null;
      await refresh();
    } on ApiException catch (e) {
      toast = 'Не удалось подключиться: ${e.message}';
    } catch (e) {
      toast = 'Не удалось подключиться: $e';
    } finally {
      nodeBusy = null;
      _notify();
    }
  }

  Future<void> connectWarp() async {
    nodeBusy = 'warp';
    toast = null;
    _notify();
    try {
      await api.connectNode('warp');
      toast = 'Активный выход: Cloudflare WARP';
      probe = null;
      await refresh();
    } on ApiException catch (e) {
      toast = 'Не удалось подключиться: ${e.message}';
    } catch (e) {
      toast = 'Не удалось подключиться: $e';
    } finally {
      nodeBusy = null;
      _notify();
    }
  }

  Future<void> toggleFavorite(NodeDto n) async {
    try {
      await api.setFavorite(n.id, !n.favorite);
      await loadNodes();
    } catch (e) {
      _fail('$e');
      _notify();
    }
  }

  /// Обновить подписки и проверить свежие узлы.
  Future<void> refreshAndCheck() async {
    nodesLoading = true;
    toast = null;
    _notify();
    Timer? poll;
    try {
      final r = await api.refreshSources();
      await loadNodes(quiet: true); // подписки обновились: новые узлы видны сразу
      // пока идёт проверка, живые узлы появляются в списке по мере готовности, а не все сразу в конце
      poll = Timer.periodic(const Duration(seconds: 2), (_) => loadNodes(quiet: true));
      final c = await api.checkNow();
      toast = '$r. $c';
    } catch (e) {
      _fail('Не удалось обновить: $e');
    } finally {
      poll?.cancel();
      nodesLoading = false;
      _notify();
    }
    await loadNodes();
  }

  // ---------------- прокси для Telegram ----------------

  List<TgProxyDto> tgProxies = const [];
  int tgTotal = 0;
  int tgAlive = 0;
  bool tgLoading = false; // идёт первая загрузка или круг проверки
  bool tgAliveOnly = true;
  bool tgFavOnly = false;
  String tgKind = ''; // '' | mtproto | socks5
  String? tgError;
  bool _tgQuietBusy = false;

  /// Лучший живой прокси: наименьший пинг среди проверенных и ответивших.
  TgProxyDto? get tgBest {
    TgProxyDto? best;
    for (final p in tgProxies) {
      if (p.ok && (best == null || p.latencyMs < best.latencyMs)) best = p;
    }
    return best;
  }

  /// quiet — подгрузка во время проверки: не трогает флаг «идёт загрузка» и не пересекается сама с собой.
  Future<void> loadTgProxies({bool quiet = false}) async {
    if (quiet) {
      if (_tgQuietBusy) return;
      _tgQuietBusy = true;
    } else if (tgLoading) {
      return;
    }
    final first = tgProxies.isEmpty && !quiet;
    if (first) {
      tgLoading = true;
      _notify();
    }
    var changed = first;
    try {
      final l = await api.tgProxies(alive: tgAliveOnly, fav: tgFavOnly, kind: tgKind);
      final before = tgProxies.map((e) => e.sig).join(',');
      changed = changed || before != l.items.map((e) => e.sig).join(',') || tgTotal != l.total || tgAlive != l.alive;
      tgProxies = l.items;
      tgTotal = l.total;
      tgAlive = l.alive;
      if (tgError != null) changed = true;
      tgError = null;
    } catch (e) {
      tgError = '$e';
      changed = true;
    } finally {
      if (quiet) {
        _tgQuietBusy = false;
      } else {
        tgLoading = false;
      }
      if (changed) _notify();
    }
  }

  void setTgView({bool? alive, bool? fav, String? kind}) {
    tgAliveOnly = alive ?? tgAliveOnly;
    tgFavOnly = fav ?? tgFavOnly;
    tgKind = kind ?? tgKind;
    _notify();
    loadTgProxies(quiet: true);
  }

  /// Проверить прокси сейчас; пока идёт, живые появляются в списке по мере готовности.
  Future<void> tgCheckNow() async {
    if (tgLoading) return;
    tgLoading = true;
    toast = null;
    _notify();
    Timer? poll = Timer.periodic(const Duration(seconds: 2), (_) => loadTgProxies(quiet: true));
    try {
      toast = await api.tgCheck();
    } on ApiException catch (e) {
      _fail(e.message.contains('уже идёт') ? 'Проверка уже идёт' : e.message);
    } catch (e) {
      _fail('Не удалось проверить: $e');
    } finally {
      poll.cancel();
      tgLoading = false;
      _notify();
    }
    await loadTgProxies(quiet: true);
  }

  Future<void> tgAddText(String text) async {
    toast = null;
    try {
      final n = await api.tgAdd(text);
      toast = 'Добавлено: $n. Проверка запущена';
      tgAliveOnly = false; // новый ещё не проверен — иначе его не видно
      _notify();
    } on ApiException catch (e) {
      _fail(e.message);
    } catch (e) {
      _fail('$e');
    }
    _notify();
    await loadTgProxies(quiet: true);
  }

  Future<void> tgRemove(TgProxyDto p) async {
    try {
      await api.tgDelete(p.id);
    } catch (e) {
      _fail('$e');
      _notify();
    }
    await loadTgProxies(quiet: true);
  }

  Future<void> tgToggleFav(TgProxyDto p) async {
    try {
      await api.tgFavorite(p.id, !p.favorite);
    } catch (e) {
      _fail('$e');
      _notify();
    }
    await loadTgProxies(quiet: true);
  }

  /// Открыть прокси в Telegram (приложение само спросит подтверждение).
  Future<void> openTg(TgProxyDto p) async {
    final err = await opener(p.link);
    if (err != null) {
      _fail(err);
    } else {
      toast = 'Подтвердите прокси в окне Telegram';
    }
    _notify();
  }

  void _notify() {
    if (!_disposed) notifyListeners();
  }

  /// Остановка при закрытии окна: демон гасим, только если запускали сами.
  Future<void> shutdown() async {
    _timer?.cancel();
    final sw = Stopwatch()..start();
    await runner.stop();
    logTiming('выход: остановка демона', {'остановка': sw.elapsed});
  }

  @override
  void dispose() {
    _disposed = true;
    _timer?.cancel();
    _measureTimer?.cancel();
    _monTimer?.cancel();
    _updTimer?.cancel();
    api.close();
    super.dispose();
  }
}
