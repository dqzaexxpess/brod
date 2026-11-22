package dev.godbtw.brod

import android.app.usage.UsageStatsManager
import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Build
import android.os.PowerManager
import java.net.Inet6Address
import java.net.InetAddress
import java.util.Locale
import java.util.TimeZone

/** Сведения об устройстве и сети для диагностического отчёта. Адреса и имена не выдаются: только их класс (частный, публичный и т. п.). */
object DeviceInfo {
    // признаки оболочек производителей: EMUI/HarmonyOS, MIUI, OneUI и т. п. Свойства читаются из getprop по имени, значения только эти
    private val shellKeys = Regex("(?i)(emui|harmony|hmos|magic|huawei|honor|miui|oneui|coloros|funtouch)")
    private val secretKeys = Regex("(?i)(serial|imei|mac|sn$|\\.id$|fingerprint|token)")

    fun collect(ctx: Context): String {
        val sb = StringBuilder()
        sb.append("Производитель: ${Build.MANUFACTURER}, бренд: ${Build.BRAND}, модель: ${Build.MODEL}, устройство: ${Build.DEVICE}\n")
        sb.append("Android ${Build.VERSION.RELEASE} (SDK ${Build.VERSION.SDK_INT}), сборка: ${Build.DISPLAY}")
        if (Build.VERSION.SDK_INT >= 23) sb.append(", патч безопасности: ${Build.VERSION.SECURITY_PATCH}")
        sb.append('\n')
        sb.append("Оболочка (свойства системы): ${shell()}\n")
        sb.append("Язык: ${Locale.getDefault()}, часовой пояс: ${TimeZone.getDefault().id}\n")
        try {
            power(ctx, sb)
        } catch (e: Exception) {
            sb.append("Питание: недоступно (${e.javaClass.simpleName})\n")
        }
        try {
            network(ctx, sb)
        } catch (e: Exception) {
            sb.append("Сеть: недоступно (${e.javaClass.simpleName})\n")
        }
        return sb.toString()
    }

    private fun shell(): String = try {
        val p = ProcessBuilder("getprop").redirectErrorStream(true).start()
        val found = ArrayList<String>()
        p.inputStream.bufferedReader().useLines { lines ->
            for (l in lines) {
                // строка вида "[ro.build.version.emui]: [EmotionUI_12.0.0]"
                val m = Regex("^\\[(.+?)\\]: \\[(.*)\\]$").find(l) ?: continue
                val k = m.groupValues[1]
                if (shellKeys.containsMatchIn(k) && !secretKeys.containsMatchIn(k) && found.size < 25) found.add("$k=${m.groupValues[2]}")
            }
        }
        p.destroy()
        if (found.isEmpty()) "признаков нет" else found.joinToString("; ")
    } catch (e: Exception) {
        "недоступно (${e.javaClass.simpleName})"
    }

    private fun power(ctx: Context, sb: StringBuilder) {
        val pm = ctx.getSystemService(Context.POWER_SERVICE) as PowerManager
        sb.append("Исключено из оптимизации батареи: ${pm.isIgnoringBatteryOptimizations(ctx.packageName)}, режим экономии: ${pm.isPowerSaveMode}\n")
        if (Build.VERSION.SDK_INT >= 28) {
            val um = ctx.getSystemService(Context.USAGE_STATS_SERVICE) as UsageStatsManager
            // 10 активно, 20 рабочий набор, 30 часто, 40 редко, 45 ограничено
            sb.append("Группа ожидания приложения: ${um.appStandbyBucket}\n")
        }
    }

    private fun addrClass(a: InetAddress): String {
        val fam = if (a is Inet6Address) "ipv6" else "ipv4"
        val cls = when {
            a.isLoopbackAddress -> "loopback"
            a.isLinkLocalAddress -> "link-local"
            a.isSiteLocalAddress -> "частный"
            a.isAnyLocalAddress -> "нулевой"
            else -> "публичный"
        }
        return "$fam/$cls"
    }

    private fun network(ctx: Context, sb: StringBuilder) {
        val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val net = cm.activeNetwork
        val caps = cm.getNetworkCapabilities(net)
        if (net == null || caps == null) {
            sb.append("Активной сети нет\n")
        } else {
            val tr = ArrayList<String>()
            if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) tr.add("wifi")
            if (caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)) tr.add("сотовая")
            if (caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)) tr.add("ethernet")
            if (caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) tr.add("vpn")
            sb.append("Активная сеть: ${tr.joinToString("+")}, не тарифицируемая: ${caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)}, ")
            sb.append("выход в интернет подтверждён системой: ${caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)}\n")
            val lp = cm.getLinkProperties(net)
            if (lp != null) {
                sb.append("DNS сети: ${lp.dnsServers.joinToString(", ") { addrClass(it) }.ifEmpty { "нет" }}")
                if (Build.VERSION.SDK_INT >= 28) sb.append(", «Частный DNS»: активен=${lp.isPrivateDnsActive}, имя сервера задано=${lp.privateDnsServerName != null}")
                sb.append('\n')
            }
        }
        // чужой VPN или наш собственный среди сетей: прямые тесты тогда идут через него
        var vpn = false
        for (n in cm.allNetworks) {
            if (cm.getNetworkCapabilities(n)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true) vpn = true
        }
        sb.append("VPN среди сетей системы: $vpn\n")
        if (Build.VERSION.SDK_INT >= 24) {
            // 1 выключена, 2 приложение в белом списке, 3 включена («Экономия трафика»)
            sb.append("Экономия трафика системы: ${cm.restrictBackgroundStatus}\n")
        }
    }
}
