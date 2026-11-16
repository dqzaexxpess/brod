package dev.godbtw.brod

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.IpPrefix
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import io.nekohasekai.androidcore.Androidcore
import io.nekohasekai.libbox.RoutePrefixIterator
import io.nekohasekai.libbox.StringIterator
import io.nekohasekai.libbox.TunOptions
import java.net.InetAddress

/** Служба VPN: даёт ядру TUN-дескриптор (Builder.establish) и защищает его сокеты (protect). Работает как служба переднего плана. */
class BrodVpnService : VpnService() {
    companion object {
        private const val CHANNEL = "brod_vpn"
        private const val NOTIFICATION_ID = 1
        const val ACTION_DISCONNECT = "dev.godbtw.brod.DISCONNECT"
        const val ACTION_CONNECT = "dev.godbtw.brod.CONNECT"

        @Volatile
        var instance: BrodVpnService? = null

        /** Возвращает запущенную службу; если её нет — запускает и ждёт до timeoutMs. */
        fun await(ctx: Context, timeoutMs: Long = 8000): BrodVpnService? {
            instance?.let { return it }
            ctx.startForegroundService(Intent(ctx, BrodVpnService::class.java))
            val end = System.currentTimeMillis() + timeoutMs
            while (System.currentTimeMillis() < end) {
                instance?.let { return it }
                Thread.sleep(50)
            }
            return null
        }

        /**
         * Запускает службу ДО создания ядра с VPN (включение из приложения). Сокеты движка (WARP, дозвон к узлу), созданные до появления службы,
         * не защищены protect() и уходят в туннель: WARP не поднимался около минуты, «подключаюсь» висело 45 с (замер на телефоне, диагностика
         * через плитку, где служба стартует первой, выход рабочий за 6–12 с). Если включение не состоится, сторож снимает службу.
         */
        fun prestart(ctx: Context) {
            await(ctx) ?: return
            android.os.Handler(android.os.Looper.getMainLooper()).postDelayed({
                if (!runCatching { Androidcore.vpnOn() }.getOrDefault(false)) stop(ctx)
            }, 20_000)
        }

        @Volatile
        private var brief: String = "VPN включён"

        private val modeNames = mapOf("blocked" to "Только заблокированное", "all" to "Всё через выход", "all-but-ru" to "Всё, кроме РФ")
        private val protoNames = mapOf(
            "vless" to "VLESS", "vmess" to "VMess", "trojan" to "Trojan", "ss" to "Shadowsocks", "shadowsocks" to "Shadowsocks",
            "hysteria2" to "Hysteria2", "hy2" to "Hysteria2", "tuic" to "TUIC", "wireguard" to "WireGuard", "warp" to "WARP",
        )

        /** Строка уведомления: «Режим · Протокол · 🇳🇱 Страна» (части без данных пропускаются). */
        private fun briefText(mode: String, proto: String, country: String): String {
            val parts = ArrayList<String>()
            modeNames[mode]?.let { parts.add(it) }
            if (proto.isNotEmpty()) parts.add(protoNames[proto.lowercase()] ?: proto.uppercase())
            if (country.length == 2 && country.all { it in 'A'..'Z' }) {
                val flag = String(country.flatMap { listOf(0x1F1E6 + (it - 'A')) }.toIntArray(), 0, 2)
                val name = java.util.Locale("", country).getDisplayCountry(java.util.Locale("ru"))
                parts.add("$flag ${name.ifEmpty { country }}")
            }
            return if (parts.isEmpty()) "VPN включён" else parts.joinToString(" · ")
        }

        /** Ядро сообщило режим, протокол или страну активного узла: обновляет текст уведомления службы. */
        fun setBrief(ctx: Context, mode: String, proto: String, country: String) {
            brief = briefText(mode, proto, country)
            instance?.let { svc -> svc.getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, svc.notification()) }
        }

        /** Выключает VPN: закрывает туннель сразу (система держит службу привязанной, поэтому onDestroy сам не придёт) и останавливает службу. */
        fun stop(ctx: Context) {
            val svc = instance
            if (svc != null) svc.shutdown() else ctx.stopService(Intent(ctx, BrodVpnService::class.java))
        }
    }

    /** Оригинал дескриптора TUN: libbox делает dup() и закрывает только свою копию, а интерфейс живёт, пока открыт любой дескриптор. */
    private var tun: ParcelFileDescriptor? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
    }

    private fun shutdown() {
        closeTun()
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    @Synchronized
    private fun closeTun() {
        runCatching { tun?.close() }
        tun = null
    }

    private fun notification(): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "VPN", NotificationManager.IMPORTANCE_LOW))
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val off = PendingIntent.getService(this, 1, Intent(this, BrodVpnService::class.java).setAction(ACTION_DISCONNECT), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setContentTitle("Brod")
            .setContentText(brief)
            .setSmallIcon(R.drawable.ic_stat_brod)
            .setContentIntent(open)
            .addAction(Notification.Action.Builder(null, "Выключить", off).build())
            .setOngoing(true)
            .build()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // на новых Android бросает исключение, если службу стартовали из фона без исключения; процесс из-за этого падать не должен
        try {
            startForeground(NOTIFICATION_ID, notification())
        } catch (e: Throwable) {
            Log.e("Brod", "startForeground: $e")
        }
        when (intent?.action) {
            // кнопка «Отключить» в шторке и плитка: выключает VPN через ядро; службу останавливает слушатель состояния (CoreHost)
            ACTION_DISCONNECT -> Thread { runCatching { Androidcore.disconnect() } }.start()
            // плитка и «Постоянный VPN» (система стартует службу сама, например после перезагрузки): поднять ядро и подключиться
            ACTION_CONNECT, SERVICE_INTERFACE -> Thread {
                try {
                    CoreHost.ensureStarted(applicationContext, withVpn = true)
                    if (!Androidcore.vpnOn()) Androidcore.connect("")
                } catch (e: Throwable) {
                    Log.e("Brod", "подключение из службы: $e")
                    shutdown()
                }
            }.start()
        }
        // не STICKY: после убийства процесса системой ядро и туннель уже погибли, оживлять пустую службу с «VPN включён» нельзя
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        instance = null
        closeTun() // без этого tun0 остаётся поднятым и перехватывает весь трафик телефона
        super.onDestroy()
    }

    override fun onRevoke() {
        // пользователь отозвал VPN в системе или включил другой: туннель закрыт системой, ядру нужно выключить VPN у себя
        Thread { runCatching { Androidcore.disconnect() }.onFailure { Log.e("Brod", "onRevoke: $it") } }.start()
        runCatching { shutdown() }.onFailure { Log.e("Brod", "onRevoke: $it") }
    }

    /** Создаёт туннель по параметрам движка и отдаёт дескриптор ядру (дальше им владеет Go). */
    fun establish(o: TunOptions): Int {
        val b = Builder().setSession("Brod").setMtu(o.mtu)
        for (p in prefixes(o.inet4Address)) b.addAddress(p.first, p.second)
        for (p in prefixes(o.inet6Address)) b.addAddress(p.first, p.second)
        if (o.autoRoute) {
            val dns = o.dnsServerAddress
            while (dns.hasNext()) b.addDnsServer(dns.next())
            val r4 = prefixes(o.inet4RouteAddress)
            if (r4.isNotEmpty()) r4.forEach { b.addRoute(it.first, it.second) } else if (prefixes(o.inet4Address).isNotEmpty()) b.addRoute("0.0.0.0", 0)
            val r6 = prefixes(o.inet6RouteAddress)
            if (r6.isNotEmpty()) r6.forEach { b.addRoute(it.first, it.second) } else if (prefixes(o.inet6Address).isNotEmpty()) b.addRoute("::", 0)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                for (p in prefixes(o.inet4RouteExcludeAddress) + prefixes(o.inet6RouteExcludeAddress)) {
                    b.excludeRoute(IpPrefix(InetAddress.getByName(p.first), p.second))
                }
            }
            val include = strings(o.includePackage)
            val exclude = strings(o.excludePackage)
            if (include.isNotEmpty()) {
                include.forEach { runCatching { b.addAllowedApplication(it) } }
                runCatching { b.addAllowedApplication(packageName) } // Brod обязан оставаться в туннеле, см. ниже
            }
            // Сам Brod из туннеля НЕ исключаем: «системный» TCP-стек движка принимает соединения слушателем в нашем процессе, и при исключении
            // приложения ответные пакеты слушателя идут мимо tun0 — весь трафик зависает (проверено на устройстве, сборка с исключением).
            // Сокеты движка к узлам защищены через protect().
            if (include.isEmpty()) exclude.filter { it != packageName }.forEach { runCatching { b.addDisallowedApplication(it) } }
        }
        b.setMetered(false)
        val pfd = b.establish() ?: throw IllegalStateException("VPN: система не создала туннель (нет разрешения на VPN?)")
        // при пересоздании (смена режима) прежний дескриптор закрываем после появления нового: окна без туннеля нет
        val old = synchronized(this) { val o = tun; tun = pfd; o }
        runCatching { old?.close() }
        return pfd.fd
    }

    private fun prefixes(it: RoutePrefixIterator): List<Pair<String, Int>> {
        val out = ArrayList<Pair<String, Int>>()
        while (it.hasNext()) {
            val p = it.next()
            out.add(p.address() to p.prefix())
        }
        return out
    }

    private fun strings(it: StringIterator): List<String> {
        val out = ArrayList<String>()
        while (it.hasNext()) out.add(it.next())
        return out
    }
}
