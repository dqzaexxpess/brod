package dev.godbtw.brod

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import io.nekohasekai.androidcore.Androidcore

/** Плитка быстрых настроек: включить или выключить VPN, не открывая приложение. */
class BrodTileService : TileService() {
    companion object {
        /** Просит систему перечитать состояние плитки (вызывается, когда VPN включается или выключается). */
        fun refresh(ctx: Context) {
            runCatching { TileService.requestListeningState(ctx, ComponentName(ctx, BrodTileService::class.java)) }
        }

        private fun vpnOn(): Boolean = BrodVpnService.instance != null && runCatching { Androidcore.vpnOn() }.getOrDefault(false)
    }

    override fun onStartListening() = update()

    private fun update() {
        val t = qsTile ?: return
        val on = vpnOn()
        t.state = if (on) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
        t.label = "Brod"
        if (Build.VERSION.SDK_INT >= 29) t.subtitle = if (on) "включён" else "выключен"
        t.updateTile()
    }

    override fun onClick() {
        if (vpnOn()) {
            // выключить защиту с заблокированного экрана нельзя: сначала разблокировка
            val off = { startService(Intent(this, BrodVpnService::class.java).setAction(BrodVpnService.ACTION_DISCONNECT)); update() }
            if (isLocked) unlockAndRun { off() } else off()
            return
        } else if (VpnService.prepare(this) != null) {
            // разрешение на VPN ещё не выдано: его можно запросить только из окна приложения
            @Suppress("DEPRECATION")
            startActivityAndCollapse(Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        } else {
            startForegroundService(Intent(this, BrodVpnService::class.java).setAction(BrodVpnService.ACTION_CONNECT))
        }
        update()
    }
}
