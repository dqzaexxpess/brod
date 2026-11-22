package dev.godbtw.brod

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.ClipData
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import androidx.core.content.FileProvider
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import io.nekohasekai.androidcore.Androidcore
import java.io.File

class MainActivity : FlutterActivity() {
    private companion object {
        const val CHANNEL = "dev.godbtw.brod/core"
        const val REQ_VPN = 1
        const val REQ_SAVE = 3
        const val REQ_OPEN = 4
        const val REQ_BATTERY = 5
        const val REQ_LOCATION = 6
        const val REQ_LOCATION_BG = 7
        const val MAX_LOG = 200 * 1024
    }

    private var pendingVpn: MethodChannel.Result? = null
    private var pendingSave: MethodChannel.Result? = null
    private var pendingSaveContent: String = ""
    private var pendingOpen: MethodChannel.Result? = null
    private var pendingBattery: MethodChannel.Result? = null
    private var pendingLocation: MethodChannel.Result? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 2)
        }
        // после убийства процесса системой запись службы и уведомление «VPN включён» остаются (службу держит привязка VPN-фреймворка), а ядра и
        // туннеля уже нет: в свежем процессе службы нет (instance == null), значит любая оставшаяся запись — мёртвая
        if (BrodVpnService.instance == null) stopService(Intent(this, BrodVpnService::class.java))
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, CHANNEL).setMethodCallHandler { call, result -> handle(call, result) }
    }

    private fun background(result: MethodChannel.Result, tag: String, block: () -> Any?) {
        Thread {
            try {
                val v = block()
                runOnUiThread { result.success(v) }
            } catch (e: Throwable) {
                runOnUiThread { result.error(tag, e.message ?: e.toString(), null) }
            }
        }.start()
    }

    private fun handle(call: MethodCall, result: MethodChannel.Result) {
        when (call.method) {
            // ядро в процессе приложения: возвращает путь unix-сокета API
            "start" -> background(result, "start") { CoreHost.ensureStarted(applicationContext) }

            // окно «Запрос на подключение» системы; true — разрешение есть
            "prepareVpn" -> {
                val intent = VpnService.prepare(this)
                if (intent == null) {
                    background(result, "prepareVpn") { BrodVpnService.prestart(applicationContext); true }
                } else {
                    pendingVpn?.success(false)
                    pendingVpn = result
                    startActivityForResult(intent, REQ_VPN)
                }
            }

            // запускаемые приложения для выбора «VPN только для…»: [{package, label}]
            "listApps" -> background(result, "apps") {
                val pm = packageManager
                val main = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
                pm.queryIntentActivities(main, 0)
                    .map { mapOf("package" to it.activityInfo.packageName, "label" to it.loadLabel(pm).toString()) }
                    .filter { it["package"] != packageName }
                    .distinctBy { it["package"] }
                    .sortedBy { it["label"]!!.lowercase() }
            }

            // tg://… и другие ссылки системным обработчиком; false — нет приложения, способного открыть
            "openUrl" -> {
                val url = call.argument<String>("url") ?: ""
                // только ссылки на прокси Telegram и веб-страницы: остальные схемы (intent:, content:, file:) не открываем
                if (Uri.parse(url).scheme?.lowercase() !in setOf("tg", "http", "https")) {
                    result.success(false)
                } else {
                    try {
                        startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                        result.success(true)
                    } catch (e: ActivityNotFoundException) {
                        result.success(false)
                    }
                }
            }

            // системные настройки VPN: «Постоянный VPN» и «Блокировать подключения без VPN» (киллсвитч на Android)
            "openVpnSettings" -> {
                try {
                    startActivity(Intent(Settings.ACTION_VPN_SETTINGS))
                    result.success(true)
                } catch (e: ActivityNotFoundException) {
                    result.success(false)
                }
            }

            // имя сети Wi-Fi (условия wifi_ssid, wifi_bssid) система отдаёт только с разрешением на точное местоположение
            "locationGranted" -> result.success(locationFullyGranted())

            // два шага: точное местоположение, затем «Разрешить всегда» (служба VPN читает имя сети в фоне, а с разрешением «только при
            // использовании» система отдаёт пустое имя; на Android 11+ второй шаг открывает страницу настроек приложения)
            "requestLocation" -> {
                if (locationFullyGranted()) {
                    result.success(true)
                } else {
                    pendingLocation?.success(false)
                    pendingLocation = result
                    if (checkSelfPermission(Manifest.permission.ACCESS_FINE_LOCATION) != PackageManager.PERMISSION_GRANTED) {
                        requestPermissions(arrayOf(Manifest.permission.ACCESS_FINE_LOCATION), REQ_LOCATION)
                    } else {
                        requestPermissions(arrayOf(Manifest.permission.ACCESS_BACKGROUND_LOCATION), REQ_LOCATION_BG)
                    }
                }
            }

            "batteryIgnored" -> result.success(getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName))

            // «Скрыть» на плашке про батарею: на HyperOS «Без ограничений» в системном окне не всегда совпадает с флагом isIgnoringBatteryOptimizations
            "batteryNoteHidden" -> result.success(getSharedPreferences("ui", MODE_PRIVATE).getBoolean("battery_note_hidden", false))
            "hideBatteryNote" -> {
                getSharedPreferences("ui", MODE_PRIVATE).edit().putBoolean("battery_note_hidden", true).apply()
                result.success(true)
            }

            "requestIgnoreBattery" -> {
                val pm = getSystemService(PowerManager::class.java)
                if (pm.isIgnoringBatteryOptimizations(packageName)) {
                    result.success(true)
                } else {
                    pendingBattery?.success(false)
                    pendingBattery = result
                    try {
                        startActivityForResult(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")), REQ_BATTERY)
                    } catch (e: ActivityNotFoundException) {
                        pendingBattery = null
                        result.success(false)
                    }
                }
            }

            // резервная копия через системный выбор файла
            "saveFile" -> {
                pendingSave?.success(null)
                pendingSave = result
                pendingSaveContent = call.argument<String>("content") ?: ""
                startActivityForResult(
                    Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("application/json")
                        .putExtra(Intent.EXTRA_TITLE, call.argument<String>("name") ?: "brod-backup.json"),
                    REQ_SAVE,
                )
            }

            "openFile" -> {
                pendingOpen?.success(null)
                pendingOpen = result
                startActivityForResult(Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("*/*"), REQ_OPEN)
            }

            // «Поделиться журналом»: хвосты core.log и engine.log одним файлом (текстом системное окно зависает, а Telegram режет сообщение по 4096 знаков)
            "shareLogs" -> background(result, "logs") {
                val dir = File(filesDir, "brod")
                val shareDir = File(cacheDir, "share").apply { mkdirs() }
                // старые выгрузки не копим: в кэше остаётся не больше одной очищенной выгрузки
                shareDir.listFiles { f -> f.name.startsWith("brod-log") }?.forEach { it.delete() }
                val out = shareDir.resolve("brod-log-${System.currentTimeMillis()}.txt")
                out.outputStream().use { o ->
                    for (name in listOf("core.log", "engine.log")) {
                        if (!File(dir, name).exists()) continue
                        // хвост читает и очищает Go (адреса, имена хостов, пароли), как для GET /v1/logs; весь файл в память не грузим
                        val tail = Androidcore.sanitizedLog(dir.path, name, MAX_LOG.toLong())
                        o.write("== $name ==\n".toByteArray())
                        o.write(tail.toByteArray())
                    }
                }
                val uri = FileProvider.getUriForFile(this, "$packageName.share", out)
                val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_STREAM, uri).putExtra(Intent.EXTRA_SUBJECT, "Журнал Brod")
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                send.clipData = ClipData.newRawUri("", uri)
                runOnUiThread { startActivity(Intent.createChooser(send, "Поделиться журналом")) }
                true
            }

            // сведения об устройстве и сети для диагностического отчёта
            "deviceInfo" -> result.success(DeviceInfo.collect(this))

            // диагностический отчёт одним файлом: устройство, результаты тестов, хвосты журналов (с датой изменения: журналы накопительные) и JSON
            "shareReport" -> background(result, "report") {
                val dir = File(filesDir, "brod")
                val shareDir = File(cacheDir, "share").apply { mkdirs() }
                shareDir.listFiles { f -> f.name.startsWith("brod-report") }?.forEach { it.delete() }
                val text = call.argument<String>("text") ?: ""
                val json = call.argument<String>("json") ?: ""
                val stamp = java.text.SimpleDateFormat("yyyyMMdd-HHmm", java.util.Locale.US).format(java.util.Date())
                val who = "${Build.MANUFACTURER}-${Build.MODEL}".replace(Regex("[^A-Za-z0-9._-]"), "_")
                val out = shareDir.resolve("brod-report-$who-$stamp.txt")
                out.outputStream().use { o ->
                    o.write("Отчёт собран: ${java.util.Date()} (время устройства)\n\n".toByteArray())
                    o.write("== Устройство ==\n${DeviceInfo.collect(this)}\n".toByteArray())
                    o.write(text.toByteArray())
                    for (name in listOf("core.log", "engine.log")) {
                        val f = File(dir, name)
                        if (!f.exists()) continue
                        val tail = Androidcore.sanitizedLog(dir.path, name, MAX_LOG.toLong())
                        o.write("\n== $name (хвост; файл изменён ${java.util.Date(f.lastModified())}, размер ${f.length()} байт) ==\n".toByteArray())
                        o.write(tail.toByteArray())
                    }
                    o.write("\n== JSON ==\n$json\n".toByteArray())
                }
                val uri = FileProvider.getUriForFile(this, "$packageName.share", out)
                val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_STREAM, uri).putExtra(Intent.EXTRA_SUBJECT, "Отчёт Brod")
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                send.clipData = ClipData.newRawUri("", uri)
                runOnUiThread { startActivity(Intent.createChooser(send, "Отправить отчёт")) }
                true
            }

            // установка скачанного обновления: файл уже проверен ядром по подписанному манифесту; здесь только передача системному установщику.
            // Без разрешения «устанавливать неизвестные приложения» открывается его страница в настройках и возвращается "permission".
            "installApk" -> {
                val path = call.argument<String>("path") ?: ""
                val dir = File(filesDir, "brod/updates").canonicalFile
                val f = File(path).canonicalFile
                if (f.parentFile != dir || !f.name.endsWith(".apk") || !f.isFile) {
                    result.error("install", "файл обновления не найден", null)
                } else if (!packageManager.canRequestPackageInstalls()) {
                    try {
                        startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")))
                        result.success("permission")
                    } catch (e: ActivityNotFoundException) {
                        result.error("install", "нет страницы разрешения установки", null)
                    }
                } else {
                    val uri = FileProvider.getUriForFile(this, "$packageName.share", f)
                    val view = Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive")
                        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    try {
                        startActivity(view)
                        result.success("started")
                    } catch (e: ActivityNotFoundException) {
                        result.error("install", "на устройстве нет установщика пакетов", null)
                    }
                }
            }

            // системное окно «Поделиться» с текстом (ссылка на узел)
            "shareText" -> {
                val send = Intent(Intent.ACTION_SEND).setType("text/plain")
                    .putExtra(Intent.EXTRA_SUBJECT, call.argument<String>("title") ?: "Brod")
                    .putExtra(Intent.EXTRA_TEXT, call.argument<String>("text") ?: "")
                startActivity(Intent.createChooser(send, "Поделиться").addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                result.success(true)
            }

            else -> result.notImplemented()
        }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQ_LOCATION) {
            val fine = grantResults.isNotEmpty() && grantResults[0] == PackageManager.PERMISSION_GRANTED
            if (fine && Build.VERSION.SDK_INT >= 29 && checkSelfPermission(Manifest.permission.ACCESS_BACKGROUND_LOCATION) != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(arrayOf(Manifest.permission.ACCESS_BACKGROUND_LOCATION), REQ_LOCATION_BG)
            } else {
                pendingLocation?.success(fine && locationFullyGranted())
                pendingLocation = null
            }
        } else if (requestCode == REQ_LOCATION_BG) {
            pendingLocation?.success(locationFullyGranted())
            pendingLocation = null
        }
    }

    private fun locationFullyGranted(): Boolean =
        checkSelfPermission(Manifest.permission.ACCESS_FINE_LOCATION) == PackageManager.PERMISSION_GRANTED &&
            (Build.VERSION.SDK_INT < 29 || checkSelfPermission(Manifest.permission.ACCESS_BACKGROUND_LOCATION) == PackageManager.PERMISSION_GRANTED)

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        when (requestCode) {
            REQ_VPN -> {
                val r = pendingVpn
                pendingVpn = null
                if (resultCode == RESULT_OK && r != null) background(r, "prepareVpn") { BrodVpnService.prestart(applicationContext); true } else r?.success(false)
            }
            REQ_BATTERY -> {
                pendingBattery?.success(getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName))
                pendingBattery = null
            }
            REQ_SAVE -> {
                val r = pendingSave
                pendingSave = null
                val uri = data?.data
                if (resultCode != RESULT_OK || uri == null) {
                    r?.success(null)
                } else {
                    val content = pendingSaveContent
                    Thread {
                        try {
                            contentResolver.openOutputStream(uri, "wt")!!.use { it.write(content.toByteArray(Charsets.UTF_8)) }
                            runOnUiThread { r?.success(uri.toString()) }
                        } catch (e: Throwable) {
                            runOnUiThread { r?.error("save", e.message ?: e.toString(), null) }
                        }
                    }.start()
                }
            }
            REQ_OPEN -> {
                val r = pendingOpen
                pendingOpen = null
                val uri = data?.data
                if (resultCode != RESULT_OK || uri == null) {
                    r?.success(null)
                } else {
                    Thread {
                        try {
                            val text = contentResolver.openInputStream(uri)!!.use { it.readBytes().toString(Charsets.UTF_8) }
                            runOnUiThread { r?.success(text) }
                        } catch (e: Throwable) {
                            runOnUiThread { r?.error("open", e.message ?: e.toString(), null) }
                        }
                    }.start()
                }
            }
            else -> {
                @Suppress("DEPRECATION")
                super.onActivityResult(requestCode, resultCode, data)
            }
        }
    }
}
