package dev.godbtw.brod

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import io.nekohasekai.androidcore.Androidcore
import io.nekohasekai.androidcore.Notifier

/** Уведомления ядра о деградации и переключении узла (P1): ядро присылает текст и кнопки, нажатие возвращается через NotifyActionReceiver. */
class Notifications(private val ctx: Context) : Notifier {
    companion object {
        const val EVENTS = "brod_events"
        const val ACTION = "dev.godbtw.brod.NOTIFY_ACTION"
        private const val ID_OFFSET = 1000 // id 1 занят уведомлением службы VPN

        fun ensureChannels(ctx: Context) {
            val nm = ctx.getSystemService(NotificationManager::class.java)
            val ch = NotificationChannel(EVENTS, "События узла", NotificationManager.IMPORTANCE_DEFAULT)
            ch.lockscreenVisibility = Notification.VISIBILITY_PRIVATE // адрес сервера на заблокированном экране не показываем
            nm.createNotificationChannel(ch)
        }
    }

    override fun show(id: Int, title: String, body: String, actions: String, important: Boolean) {
        val nm = ctx.getSystemService(NotificationManager::class.java)
        val open = PendingIntent.getActivity(ctx, 0, Intent(ctx, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val b = Notification.Builder(ctx, EVENTS)
            .setSmallIcon(R.drawable.ic_stat_brod)
            .setContentTitle(title)
            .setContentText(body)
            .setStyle(Notification.BigTextStyle().bigText(body))
            .setContentIntent(open)
            .setAutoCancel(true)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            // на заблокированном экране: без протокола, адреса и порта сервера и имени цели
            .setPublicVersion(
                Notification.Builder(ctx, EVENTS)
                    .setSmallIcon(R.drawable.ic_stat_brod)
                    .setContentTitle("Brod")
                    .setContentText("Событие узла")
                    .build()
            )
        if (actions.isNotEmpty()) {
            for (a in actions.split('\u001e')) {
                val parts = a.split('\u001f', limit = 2)
                if (parts.size != 2) continue
                val i = Intent(ctx, NotifyActionReceiver::class.java).setAction(ACTION).putExtra("id", id).putExtra("key", parts[0])
                val pi = PendingIntent.getBroadcast(ctx, id * 31 + parts[0].hashCode(), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
                b.addAction(Notification.Action.Builder(null, parts[1], pi).build())
            }
        }
        nm.notify(id + ID_OFFSET, b.build())
    }

    override fun cancel(id: Int) {
        ctx.getSystemService(NotificationManager::class.java).cancel(id + ID_OFFSET)
    }
}

/** Нажатие кнопки уведомления («Переключить на …») — явное действие пользователя: передаётся ядру. */
class NotifyActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val id = intent.getIntExtra("id", 0)
        val key = intent.getStringExtra("key") ?: return
        val pending = goAsync()
        Thread {
            try {
                Androidcore.notifyAction(id, key)
            } finally {
                runCatching { Notifications(context).cancel(id) }
                pending.finish()
            }
        }.start()
    }
}
