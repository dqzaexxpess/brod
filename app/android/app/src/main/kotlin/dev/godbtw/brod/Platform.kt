package dev.godbtw.brod

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Process
import android.util.Log
import android.telephony.TelephonyManager
import io.nekohasekai.androidcore.Androidcore
import io.nekohasekai.libbox.BridgeOptions
import io.nekohasekai.libbox.BridgeSession
import io.nekohasekai.libbox.ConnectionOwner
import io.nekohasekai.libbox.InterfaceUpdateListener
import io.nekohasekai.libbox.Libbox
import io.nekohasekai.libbox.LocalDNSTransport
import io.nekohasekai.libbox.NeighborUpdateListener
import io.nekohasekai.libbox.NetworkInterface
import io.nekohasekai.libbox.NetworkInterfaceIterator
import io.nekohasekai.libbox.Notification
import io.nekohasekai.libbox.PlatformInterface
import io.nekohasekai.libbox.PlatformUser
import io.nekohasekai.libbox.ShellSession
import io.nekohasekai.libbox.StringIterator
import io.nekohasekai.libbox.TunOptions
import io.nekohasekai.libbox.WIFIState
import java.net.InetAddress
import java.net.InetSocketAddress

class StringList(private val items: List<String>) : StringIterator {
    private var i = 0
    override fun hasNext() = i < items.size
    override fun len() = items.size
    override fun next() = items[i++]
}

/** Платформенный интерфейс движка: TUN и защита сокетов даёт VpnService, о смене сети сообщает ConnectivityManager. */
class Platform(private val ctx: Context) : PlatformInterface {
    private val cm = ctx.getSystemService(ConnectivityManager::class.java)
    private val unsupported = UnsupportedOperationException("не поддерживается на Android")

    // ---- сеть: физические (не VPN) сети; по ним строится идентификатор сети для истории проверок и «интерфейс по умолчанию» ----
    private val lock = Any()
    private val nets = LinkedHashMap<Network, Pair<LinkProperties, NetworkCapabilities?>>()
    private val listeners = ArrayList<InterfaceUpdateListener>()
    private var started = false

    /** Приложение уже получило от системы хотя бы одно сообщение о сети (идентификатор сети известен). */
    @Volatile var networkKnown = false

    fun startNetworkWatch() {
        synchronized(lock) { if (started) return; started = true }
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        cm.registerNetworkCallback(req, object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = update(network)
            override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = update(network)
            override fun onCapabilitiesChanged(network: Network, nc: NetworkCapabilities) = update(network)
            override fun onLost(network: Network) {
                synchronized(lock) { nets.remove(network) }
                publish()
            }
        })
    }

    private fun update(n: Network) {
        val lp = cm.getLinkProperties(n) ?: return
        synchronized(lock) { nets[n] = lp to cm.getNetworkCapabilities(n) }
        publish()
    }

    /** Предпочитаем Wi-Fi, затем сотовую сеть, затем что есть. */
    private fun best(): Map.Entry<Network, Pair<LinkProperties, NetworkCapabilities?>>? = synchronized(lock) {
        nets.entries.sortedBy { e ->
            val nc = e.value.second
            when {
                nc?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) == true -> 0
                nc?.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) == true -> 1
                nc?.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) == true -> 2
                else -> 3
            }
        }.firstOrNull()
    }

    private fun typeOf(nc: NetworkCapabilities?) = when {
        nc?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) == true -> Libbox.InterfaceTypeWIFI
        nc?.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) == true -> Libbox.InterfaceTypeCellular
        nc?.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) == true -> Libbox.InterfaceTypeEthernet
        else -> Libbox.InterfaceTypeOther
    }

    // Os.if_nametoindex — один ioctl SIOCGIFINDEX. Прежний java.net.NetworkInterface.getByName перебирал все интерфейсы через getifaddrs, то есть через сокет
    // netlink, на который у приложения нет права (SELinux, Android 11+): каждое сообщение системы о сети давало отказ, запись в журнал аудита и индекс 0.
    private fun indexOf(name: String): Int = runCatching { android.system.Os.if_nametoindex(name) }.getOrDefault(0)

    private fun publish() {
        val b = best()
        val lp = b?.value?.first
        val name = lp?.interfaceName ?: ""
        val nc = b?.value?.second
        // идентификатор сети: тип + шлюз (разные Wi-Fi сети дают разные шлюзы; SSID без разрешения на геолокацию не получить)
        val gw = lp?.routes?.firstOrNull { it.isDefaultRoute }?.gateway?.hostAddress ?: name
        val kind = when (typeOf(nc)) { Libbox.InterfaceTypeWIFI -> "wifi"; Libbox.InterfaceTypeCellular -> "cell"; Libbox.InterfaceTypeEthernet -> "eth"; else -> "net" }
        // у сотовой сети шлюз внутри оператора меняется от сеанса к сеансу и почти ничего не говорит о сети: ключ истории проверок — оператор (MCC+MNC,
        // разрешений не требует); результаты одного оператора действуют и после переподключения
        val operator = if (kind == "cell") (ctx.getSystemService(TelephonyManager::class.java)?.networkOperator ?: "") else ""
        Androidcore.setNetworkID(if (b == null) "offline" else "$kind:${operator.ifEmpty { gw }}")
        networkKnown = true
        // DNS сети (IPv4, не loopback): у Go и движка на Android нет /etc/resolv.conf
        val dns = lp?.dnsServers?.firstOrNull { it is java.net.Inet4Address && !it.isLoopbackAddress }?.hostAddress
        Androidcore.setDNS(dns ?: "")
        val l = synchronized(lock) { ArrayList(listeners) }
        val metered = nc?.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) == false
        Androidcore.setMetered(metered)
        for (x in l) x.updateDefaultInterface(name, if (name.isEmpty()) -1 else indexOf(name), metered, false)
    }

    override fun startDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        synchronized(lock) { listeners.add(listener) }
        startNetworkWatch()
        publish()
    }

    override fun closeDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        synchronized(lock) { listeners.remove(listener) }
    }

    override fun getInterfaces(): NetworkInterfaceIterator {
        val list = synchronized(lock) { nets.values.toList() }.mapNotNull { (lp, nc) ->
            val name = lp.interfaceName ?: return@mapNotNull null
            NetworkInterface().also {
                it.index = indexOf(name)
                it.mtu = if (lp.mtu > 0) lp.mtu else 1500
                it.name = name
                it.addresses = StringList(lp.linkAddresses.map { a -> "${a.address.hostAddress}/${a.prefixLength}" })
                it.flags = 1 or 2 or 16 or 32 // up, broadcast, multicast, running
                it.type = typeOf(nc)
                it.dnsServer = StringList(lp.dnsServers.mapNotNull { d -> d.hostAddress })
                it.gateway = StringList(lp.routes.filter { r -> r.isDefaultRoute }.mapNotNull { r -> r.gateway?.hostAddress })
                it.metered = nc?.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) == false
            }
        }
        return object : NetworkInterfaceIterator {
            private var i = 0
            override fun hasNext() = i < list.size
            override fun next() = list[i++]
        }
    }

    // ---- туннель и защита сокетов ----
    override fun usePlatformAutoDetectInterfaceControl() = true

    override fun autoDetectInterfaceControl(fd: Int) {
        // без запущенной службы VPN защищать нечего: сокеты и так идут мимо туннеля
        val svc = BrodVpnService.instance
        if (svc != null && !svc.protect(fd)) Log.w("Brod", "protect($fd) не удался: сокет может уйти в туннель")
    }

    override fun openTun(options: TunOptions): Int {
        val svc = BrodVpnService.await(ctx) ?: throw IllegalStateException("служба VPN не запустилась")
        return svc.establish(options)
    }

    // ---- не нужно на Android ----
    override fun localDNSTransport(): LocalDNSTransport? = null
    // ---- какое приложение владеет соединением: нужно правилам по package_name (движок спрашивает при find_process) ----
    override fun findConnectionOwner(ipProtocol: Int, sourceAddress: String, sourcePort: Int, destinationAddress: String, destinationPort: Int): ConnectionOwner {
        if (Build.VERSION.SDK_INT < 29) throw unsupported // getConnectionOwnerUid появился в Android 10
        val uid = cm.getConnectionOwnerUid(
            ipProtocol,
            InetSocketAddress(InetAddress.getByName(sourceAddress), sourcePort),
            InetSocketAddress(InetAddress.getByName(destinationAddress), destinationPort),
        )
        if (uid == Process.INVALID_UID) throw IllegalStateException("владелец соединения не найден")
        val packages = ctx.packageManager.getPackagesForUid(uid)?.toList() ?: emptyList()
        return ConnectionOwner().apply {
            userId = uid
            userName = packages.firstOrNull() ?: ""
            setAndroidPackageNames(StringList(packages))
        }
    }
    override fun underNetworkExtension() = false
    override fun includeAllNetworks() = false
    // Состояние Wi-Fi для условий wifi_ssid и wifi_bssid: движок спрашивает при каждой смене сети по умолчанию. Без разрешения на точное
    // местоположение (и включённой геолокации) система отдаёт «<unknown ssid>»: тогда состояния нет (null — сеть не Wi-Fi или имя скрыто).
    override fun readWIFIState(): WIFIState? = try {
        val wm = ctx.applicationContext.getSystemService(WifiManager::class.java)
        @Suppress("DEPRECATION")
        val info = wm?.connectionInfo
        val ssid = (info?.ssid ?: "").trim('"')
        val bssid = info?.bssid ?: ""
        // имя и адрес точки Wi-Fi — данные о местоположении: в системный журнал (он попадает в отчёты об ошибках) значения не пишем
        Log.i("BrodWifi", "readWIFIState: ssid=${if (ssid.isEmpty()) "-" else "set"} bssid=${if (bssid.isEmpty()) "-" else "set"}")
        if (ssid.isEmpty() || ssid == WifiManager.UNKNOWN_SSID) null else Libbox.newWIFIState(ssid, bssid)
    } catch (e: Exception) {
        Log.w("BrodWifi", "readWIFIState: $e")
        null
    }
    override fun clearDNSCache() {}
    override fun sendNotification(notification: Notification) {}
    override fun cancelNotification(identifier: String, typeID: Int) {}
    override fun startNeighborMonitor(listener: NeighborUpdateListener) {}
    override fun closeNeighborMonitor(listener: NeighborUpdateListener) {}
    override fun registerMyInterface(name: String) {}
    override fun usePlatformShell() = false
    override fun checkPlatformShell() = throw unsupported
    override fun openShellSession(user: PlatformUser, command: String, environ: StringIterator, term: String, rows: Int, cols: Int): ShellSession = throw unsupported
    override fun lookupUser(username: String): PlatformUser = throw unsupported
    override fun lookupSFTPServer(): String = throw unsupported
    override fun readSystemSSHHostKey(): String = throw unsupported
    override fun tailscaleHostname() = ""
    override fun usePlatformBridge() = false
    override fun createBridge(options: BridgeOptions): BridgeSession = throw unsupported
}
