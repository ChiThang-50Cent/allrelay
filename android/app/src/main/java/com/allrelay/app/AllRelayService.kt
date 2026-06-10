package com.allrelay.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.IBinder
import android.util.Log

/**
 * AllRelayService - Background service for AllRelay
 *
 * This service manages the AllRelay server and handles stream toggles.
 * It runs as a foreground service to maintain persistent connection.
 *
 * For rooted devices, it can also run as a Magisk daemon for
 * background operation without notification.
 */
class AllRelayService : Service() {

    companion object {
        private const val TAG = "AllRelayService"
        private const val CHANNEL_ID = "allrelay_service"
        private const val NOTIFICATION_ID = 1

        const val ACTION_START = "com.allrelay.START"
        const val ACTION_STOP = "com.allrelay.STOP"
        const val ACTION_TOGGLE_STREAM = "com.allrelay.TOGGLE_STREAM"
    }

    private var isRunning = false
    private val activeStreams = mutableSetOf<String>()

    private val toggleReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action == ACTION_TOGGLE_STREAM) {
                val streamName = intent.getStringExtra("stream_name") ?: return
                val enabled = intent.getBooleanExtra("enabled", false)
                toggleStream(streamName, enabled)
            }
        }
    }

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
        registerToggleReceiver()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> startService()
            ACTION_STOP -> stopService()
        }
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? {
        return null
    }

    override fun onDestroy() {
        super.onDestroy()
        stopService()
        unregisterReceiver(toggleReceiver)
    }

    private fun startService() {
        if (isRunning) return

        Log.i(TAG, "Starting AllRelay service")

        // Start foreground service
        val notification = createNotification("AllRelay is running")
        startForeground(NOTIFICATION_ID, notification)

        // Start scrcpy server in Wi-Fi mode
        startScrcpyServer()

        isRunning = true
    }

    private fun stopService() {
        if (!isRunning) return

        Log.i(TAG, "Stopping AllRelay service")

        // Stop scrcpy server
        stopScrcpyServer()

        isRunning = false
        stopForeground(true)
        stopSelf()
    }

    private fun toggleStream(streamName: String, enabled: Boolean) {
        Log.i(TAG, "Toggle stream: $streamName = $enabled")

        if (enabled) {
            activeStreams.add(streamName)
        } else {
            activeStreams.remove(streamName)
        }

        // Update scrcpy server with new stream configuration
        updateStreamConfig()
    }

    private fun startScrcpyServer() {
        try {
            // Build scrcpy-server command with Wi-Fi mode
            val cmd = buildString {
                append("app_process / com.genymobile.scrcpy.Server")
                append(" 4.0") // version
                append(" wifi_mode=true")
                append(" wifi_port=5000")
                append(" video=${activeStreams.contains("screen") || activeStreams.contains("camera")}")
                append(" audio=${activeStreams.contains("mic") || activeStreams.contains("speaker")}")
                append(" control=true")
                append(" send_device_meta=true")
                append(" send_frame_meta=true")
                append(" send_dummy_byte=true")
                append(" send_stream_meta=true")
            }

            Log.i(TAG, "Starting scrcpy-server: $cmd")

            // Execute as root if available, otherwise as shell
            val process = Runtime.getRuntime().exec(arrayOf("sh", "-c", cmd))
            Log.i(TAG, "scrcpy-server started")

        } catch (e: Exception) {
            Log.e(TAG, "Failed to start scrcpy-server", e)
        }
    }

    private fun stopScrcpyServer() {
        try {
            Runtime.getRuntime().exec(arrayOf("sh", "-c", "pkill -f 'scrcpy-server'"))
            Log.i(TAG, "scrcpy-server stopped")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to stop scrcpy-server", e)
        }
    }

    private fun updateStreamConfig() {
        // Restart server with new configuration
        if (isRunning) {
            stopScrcpyServer()
            startScrcpyServer()
        }
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                "AllRelay Service",
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "AllRelay background service"
            }
            val notificationManager = getSystemService(NotificationManager::class.java)
            notificationManager.createNotificationChannel(channel)
        }
    }

    private fun createNotification(text: String): Notification {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("AllRelay")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.ic_dialog_info)
                .build()
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
                .setContentTitle("AllRelay")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.ic_dialog_info)
                .build()
        }
    }

    private fun registerToggleReceiver() {
        val filter = IntentFilter(ACTION_TOGGLE_STREAM)
        registerReceiver(toggleReceiver, filter)
    }
}
