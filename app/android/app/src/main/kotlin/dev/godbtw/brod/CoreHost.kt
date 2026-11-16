package dev.godbtw.brod

import android.content.Context
import io.nekohasekai.androidcore.Androidcore
import io.nekohasekai.androidcore.StateListener
import java.io.File

/**
 * Общий вход для запуска ядра: его вызывают и интерфейс (MainActivity), и служба VPN («Постоянный VPN» после перезагрузки, плитка), поэтому
 * запуск идемпотентен: ядро одно на процесс.
 */
object CoreHost {
    @Volatile
    private var platform: Platform? = null
    private var listenerSet = false
    private var wasOn = false

    @Synchronized
    fun platform(ctx: Context): Platform = platform ?: Platform(ctx.applicationContext).also { platform = it }

    /** Запускает ядро (если ещё не запущено) и возвращает путь unix-сокета API. */
    @Synchronized
    fun ensureStarted(ctx: Context, withVpn: Boolean = false): String {
        val app = ctx.applicationContext
        val p = platform(app)
        p.startNetworkWatch()
        // ядро выбирает профиль WARP по идентификатору сети при создании: подождать первое сообщение системы о сети (обычно миллисекунды),
        // иначе ядро стартует с «неизвестной» сетью и пересоздаётся через 20 секунд, когда сеть уже известна
        for (i in 0 until 30) { if (p.networkKnown) break; Thread.sleep(50) }
        Notifications.ensureChannels(app)
        // withVpn: ядро ещё не запущено и VPN всё равно включать (служба VPN уже работает) — одно создание ядра вместо двух
        val dir = File(app.filesDir, "brod").path
        val sock = if (withVpn) Androidcore.startVPN(dir, p) else Androidcore.start(dir, p)
        if (!listenerSet) {
            listenerSet = true
            Androidcore.setNotifier(Notifications(app))
            // служба переднего плана (и её уведомление) живёт, пока VPN включён; первое сообщение слушателя — текущее состояние, его не считаем выключением
            Androidcore.setStateListener(object : StateListener {
                override fun onVPN(on: Boolean) {
                    if (on) {
                        wasOn = true
                    } else if (wasOn) {
                        wasOn = false
                        BrodVpnService.stop(app)
                    }
                    BrodTileService.refresh(app)
                }

                override fun onBrief(mode: String, proto: String, country: String) = BrodVpnService.setBrief(app, mode, proto, country)
            })
        }
        return sock
    }
}
