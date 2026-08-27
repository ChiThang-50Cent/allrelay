package com.allrelay.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.net.wifi.WifiManager
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.Executors
import java.util.concurrent.RejectedExecutionException
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.ThreadPoolExecutor
import java.util.concurrent.TimeUnit

class AllRelayService : Service() {
    private val serverExecutor = Executors.newSingleThreadExecutor()
    private val clientExecutor = ThreadPoolExecutor(
        4,
        4,
        0L,
        TimeUnit.MILLISECONDS,
        ArrayBlockingQueue(MAX_QUEUED_CLIENTS),
    )
    private val scheduler = Executors.newSingleThreadScheduledExecutor()
    private val adbLock = Any()

    @Volatile
    private var serverSocket: ServerSocket? = null
    private var autoDisableFuture: ScheduledFuture<*>? = null
    private var autoDisableAtMs: Long = 0
    private var adbStatus = RootDaemonManager.WirelessAdbStatus(
        enabled = false,
        listening = false,
        port = null,
        message = "ADB over Wi‑Fi disabled",
    )

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
        startForeground(NOTIFICATION_ID, buildNotification())
        restoreAdbTimeoutIfNeeded()
        startControlServer()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_SYNC_ADB_STATUS) {
            syncAdbStatusFromActivity(intent)
        }
        return START_STICKY
    }

    override fun onDestroy() {
        super.onDestroy()
        try {
            serverSocket?.close()
        } catch (_: Exception) {
        }
        serverSocket = null
        synchronized(adbLock) {
            autoDisableFuture?.cancel(true)
            autoDisableFuture = null
        }
        scheduler.shutdownNow()
        clientExecutor.shutdownNow()
        serverExecutor.shutdownNow()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun startControlServer() {
        serverExecutor.execute {
            try {
                val socket = ServerSocket(CONTROL_PORT)
                serverSocket = socket
                while (!socket.isClosed) {
                    val client = socket.accept()
                    try {
                        clientExecutor.execute {
                            handleClient(client)
                        }
                    } catch (_: RejectedExecutionException) {
                        client.close()
                    }
                }
            } catch (_: Exception) {
                // Service will be restarted by Android if needed.
            }
        }
    }

    private fun handleClient(client: Socket) {
        client.use { socket ->
            socket.soTimeout = 5000
            val reader = BufferedReader(InputStreamReader(socket.getInputStream(), Charsets.UTF_8))
            val writer = OutputStreamWriter(socket.getOutputStream(), Charsets.UTF_8)

            val requestLine = reader.readLine() ?: return
            val parts = requestLine.split(" ")
            if (parts.size < 2) {
                writeJsonResponse(writer, 400, jsonError("Invalid request line"))
                return
            }
            val method = parts[0].trim()
            val path = parts[1].trim()

            var contentLength = 0
            while (true) {
                val line = reader.readLine() ?: break
                if (line.isEmpty()) break
                val idx = line.indexOf(':')
                if (idx <= 0) continue
                val headerName = line.substring(0, idx).trim().lowercase()
                val headerValue = line.substring(idx + 1).trim()
                if (headerName == "content-length") {
                    contentLength = headerValue.toIntOrNull() ?: 0
                }
            }
            if (contentLength > MAX_REQUEST_BODY_BYTES) {
                writeJsonResponse(writer, 400, jsonError("Request body too large"))
                return
            }

            val body = if (contentLength > 0) {
                val chars = CharArray(contentLength)
                var read = 0
                while (read < contentLength) {
                    val n = reader.read(chars, read, contentLength - read)
                    if (n <= 0) break
                    read += n
                }
                String(chars, 0, read)
            } else {
                ""
            }

            val response = try {
                handleRequest(method, path, body)
            } catch (e: Exception) {
                Pair(500, jsonError(e.message ?: e.javaClass.simpleName))
            }
            writeJsonResponse(writer, response.first, response.second)
        }
    }

    private fun handleRequest(method: String, path: String, body: String): Pair<Int, JSONObject> {
        return when {
            method == "GET" && path == "/health" -> 200 to JSONObject()
                .put("ok", true)
                .put("servicePort", CONTROL_PORT)
                .put("ip", currentWifiIp())

            // Status is cached so dashboard refreshes never invoke su.
            method == "GET" && path == "/adb/status" -> 200 to buildAdbStatusJson(cachedAdbStatus())

            // The desktop sends this only while its adb server still sees this
            // device. It restores the old "connected hosts keep ADB alive"
            // behavior without polling su on the phone.
            method == "POST" && path == "/adb/heartbeat" -> {
                val req = if (body.isBlank()) JSONObject() else JSONObject(body)
                val status = renewAdbLease(req.optInt("timeoutSeconds", DEFAULT_ADB_TIMEOUT_SECONDS))
                if (status == null) {
                    409 to buildAdbStatusJson(cachedAdbStatus()).put("message", "ADB over Wi‑Fi disabled")
                } else {
                    200 to buildAdbStatusJson(status)
                }
            }

            method == "POST" && path == "/adb/enable" -> {
                val req = if (body.isBlank()) JSONObject() else JSONObject(body)
                val port = req.optInt("port", RootDaemonManager.ADB_TCP_DEFAULT_PORT)
                val timeoutSeconds = req.optInt("timeoutSeconds", DEFAULT_ADB_TIMEOUT_SECONDS)
                val status = RootDaemonManager.enableWirelessAdb(port)
                updateAdbStatus(status)
                if (!status.enabled) {
                    500 to buildAdbStatusJson(status).put("message", status.message)
                } else {
                    scheduleAutoDisable(timeoutSeconds)
                    200 to buildAdbStatusJson(status)
                }
            }

            method == "POST" && path == "/adb/disable" -> {
                val status = disableWirelessAdbAndClearLease()
                200 to buildAdbStatusJson(status)
            }

            method == "POST" && path == "/adb/authorize" -> {
                val req = if (body.isBlank()) JSONObject() else JSONObject(body)
                val key = req.opt("key") as? String
                if (key == null || RootDaemonManager.normalizeAdbKeyLine(key) == null) {
                    400 to jsonError("Invalid 'key' field")
                } else if (RootDaemonManager.authorizeAdbKey(key)) {
                    200 to JSONObject().put("ok", true).put("message", "Host key authorized; adbd reloaded")
                } else {
                    500 to jsonError("Failed to authorize host key or reload adbd")
                }
            }

            else -> 404 to jsonError("Not found")
        }
    }

    private fun cachedAdbStatus(): RootDaemonManager.WirelessAdbStatus {
        synchronized(adbLock) {
            return adbStatus
        }
    }

    private fun updateAdbStatus(status: RootDaemonManager.WirelessAdbStatus) {
        synchronized(adbLock) {
            adbStatus = status
            getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
                .edit()
                .putBoolean(PREF_ADB_ENABLED, status.enabled)
                .putInt(PREF_ADB_PORT, status.port ?: RootDaemonManager.ADB_TCP_DEFAULT_PORT)
                .apply()
        }
    }

    private fun buildAdbStatusJson(status: RootDaemonManager.WirelessAdbStatus): JSONObject {
        synchronized(adbLock) {
            return JSONObject()
                .put("ok", true)
                .put("ip", currentWifiIp())
                .put("controlPort", CONTROL_PORT)
                .put("enabled", status.enabled)
                .put("listening", status.listening)
                .put("port", status.port)
                .put("message", status.message)
                .put("autoDisableAtMs", autoDisableAtMs)
        }
    }

    private fun scheduleAutoDisable(timeoutSeconds: Int) {
        synchronized(adbLock) {
            scheduleAutoDisableLocked(timeoutSeconds)
        }
    }

    private fun renewAdbLease(timeoutSeconds: Int): RootDaemonManager.WirelessAdbStatus? {
        synchronized(adbLock) {
            if (!adbStatus.enabled) {
                return null
            }
            scheduleAutoDisableLocked(timeoutSeconds)
            return adbStatus
        }
    }

    private fun scheduleAutoDisableLocked(timeoutSeconds: Int) {
        val safeTimeout = timeoutSeconds.coerceAtLeast(30)
        val disableAt = System.currentTimeMillis() + safeTimeout * 1000L
        autoDisableFuture?.cancel(false)
        autoDisableAtMs = disableAt
        persistAdbTimer(disableAt)
        scheduleAutoDisableAt(disableAt)
    }

    private fun scheduleAutoDisableAt(disableAt: Long) {
        val delayMs = (disableAt - System.currentTimeMillis()).coerceAtLeast(0)
        autoDisableFuture = scheduler.schedule({
            synchronized(adbLock) {
                if (autoDisableAtMs == disableAt) {
                    disableWirelessAdbAndClearLease()
                }
            }
        }, delayMs, TimeUnit.MILLISECONDS)
    }

    private fun cancelAutoDisable() {
        synchronized(adbLock) {
            autoDisableFuture?.cancel(false)
            autoDisableFuture = null
            autoDisableAtMs = 0L
            clearAdbTimer()
        }
    }

    private fun disableWirelessAdbAndClearLease(): RootDaemonManager.WirelessAdbStatus {
        synchronized(adbLock) {
            autoDisableFuture?.cancel(false)
            autoDisableFuture = null
            val status = RootDaemonManager.disableWirelessAdb()
            updateAdbStatus(status)
            clearAdbTimer()
            return status
        }
    }

    private fun restoreAdbTimeoutIfNeeded() {
        val prefs = getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val disableAt = prefs.getLong(PREF_ADB_DISABLE_AT_MS, 0L)
        if (disableAt <= 0L) {
            return
        }

        val storedEnabled = if (prefs.contains(PREF_ADB_ENABLED)) {
            prefs.getBoolean(PREF_ADB_ENABLED, false)
        } else {
            null
        }
        when (adbLeaseRestoreAction(disableAt, System.currentTimeMillis(), storedEnabled)) {
            AdbLeaseRestoreAction.DISABLE -> disableWirelessAdbAndClearLease()

            AdbLeaseRestoreAction.CLEAR -> clearAdbTimer()

            AdbLeaseRestoreAction.RESTORE -> {
                val port = prefs.getInt(PREF_ADB_PORT, RootDaemonManager.ADB_TCP_DEFAULT_PORT)
                synchronized(adbLock) {
                    adbStatus = RootDaemonManager.WirelessAdbStatus(
                        enabled = true,
                        listening = false,
                        port = port,
                        message = "ADB lease restored; awaiting host check",
                    )
                    if (storedEnabled == null) {
                        prefs.edit().putBoolean(PREF_ADB_ENABLED, true).apply()
                    }
                    autoDisableAtMs = disableAt
                    scheduleAutoDisableAt(disableAt)
                }
            }
        }
    }

    private fun persistAdbTimer(disableAtMs: Long) {
        getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .edit()
            .putLong(PREF_ADB_DISABLE_AT_MS, disableAtMs)
            .apply()
    }

    private fun clearAdbTimer() {
        synchronized(adbLock) {
            autoDisableAtMs = 0L
            getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
                .edit()
                .remove(PREF_ADB_DISABLE_AT_MS)
                .apply()
        }
    }

    private fun syncAdbStatusFromActivity(intent: Intent) {
        val status = RootDaemonManager.WirelessAdbStatus(
            enabled = intent.getBooleanExtra(EXTRA_ADB_ENABLED, false),
            listening = intent.getBooleanExtra(EXTRA_ADB_LISTENING, false),
            port = intent.getIntExtra(EXTRA_ADB_PORT, -1).takeIf { it > 0 },
            message = intent.getStringExtra(EXTRA_ADB_MESSAGE).orEmpty(),
        )
        synchronized(adbLock) {
            updateAdbStatus(status)
            if (status.enabled) {
                if (autoDisableAtMs <= System.currentTimeMillis()) {
                    scheduleAutoDisableLocked(DEFAULT_ADB_TIMEOUT_SECONDS)
                }
            } else {
                cancelAutoDisable()
            }
        }
    }

    private fun writeJsonResponse(writer: OutputStreamWriter, statusCode: Int, body: JSONObject) {
        val bytes = body.toString().toByteArray(Charsets.UTF_8)
        writer.write("HTTP/1.1 $statusCode ${statusText(statusCode)}\r\n")
        writer.write("Content-Type: application/json\r\n")
        writer.write("Content-Length: ${bytes.size}\r\n")
        writer.write("Connection: close\r\n")
        writer.write("\r\n")
        writer.write(String(bytes, Charsets.UTF_8))
        writer.flush()
    }

    private fun jsonError(message: String): JSONObject {
        return JSONObject()
            .put("ok", false)
            .put("message", message)
    }

    private fun statusText(code: Int): String = when (code) {
        200 -> "OK"
        400 -> "Bad Request"
        404 -> "Not Found"
        409 -> "Conflict"
        else -> "Internal Server Error"
    }

    @Suppress("DEPRECATION")
    private fun currentWifiIp(): String {
        return try {
            val wifiManager = applicationContext.getSystemService(WIFI_SERVICE) as WifiManager
            val ip = wifiManager.connectionInfo.ipAddress
            String.format(
                "%d.%d.%d.%d",
                ip and 0xff,
                ip shr 8 and 0xff,
                ip shr 16 and 0xff,
                ip shr 24 and 0xff,
            )
        } catch (_: Exception) {
            ""
        }
    }

    private fun createNotificationChannel() {
        val channel = NotificationChannel(
            CHANNEL_ID,
            "AllRelay Background",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Keeps AllRelay remote control bridge available"
        }
        val manager = getSystemService(NotificationManager::class.java)
        manager?.createNotificationChannel(channel)
    }

    private fun buildNotification(): Notification {
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("AllRelay")
            .setContentText("Remote control bridge is available")
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .setOngoing(true)
            .build()
    }

    companion object {
        const val CONTROL_PORT = 5008
        const val DEFAULT_ADB_TIMEOUT_SECONDS = 15 * 60
        private const val MAX_QUEUED_CLIENTS = 16
        private const val MAX_REQUEST_BODY_BYTES = 8 * 1024

        private const val NOTIFICATION_ID = 4001
        private const val CHANNEL_ID = "allrelay-background"
        private const val PREFS_NAME = "allrelay_service"
        private const val PREF_ADB_DISABLE_AT_MS = "adb_disable_at_ms"
        private const val PREF_ADB_ENABLED = "adb_enabled"
        private const val PREF_ADB_PORT = "adb_port"
        private const val ACTION_SYNC_ADB_STATUS = "com.allrelay.app.SYNC_ADB_STATUS"
        private const val EXTRA_ADB_ENABLED = "adbEnabled"
        private const val EXTRA_ADB_LISTENING = "adbListening"
        private const val EXTRA_ADB_PORT = "adbPort"
        private const val EXTRA_ADB_MESSAGE = "adbMessage"

        internal enum class AdbLeaseRestoreAction {
            DISABLE,
            CLEAR,
            RESTORE,
        }

        internal fun adbLeaseRestoreAction(
            disableAtMs: Long,
            nowMs: Long,
            storedEnabled: Boolean?,
        ): AdbLeaseRestoreAction = when {
            disableAtMs <= 0L -> AdbLeaseRestoreAction.CLEAR
            disableAtMs <= nowMs -> AdbLeaseRestoreAction.DISABLE
            storedEnabled == false -> AdbLeaseRestoreAction.CLEAR
            else -> AdbLeaseRestoreAction.RESTORE
        }

        fun ensureRunning(context: Context) {
            val intent = Intent(context, AllRelayService::class.java)
            ContextCompat.startForegroundService(context, intent)
        }

        fun syncAdbStatus(context: Context, status: RootDaemonManager.WirelessAdbStatus) {
            val intent = Intent(context, AllRelayService::class.java).apply {
                action = ACTION_SYNC_ADB_STATUS
                putExtra(EXTRA_ADB_ENABLED, status.enabled)
                putExtra(EXTRA_ADB_LISTENING, status.listening)
                putExtra(EXTRA_ADB_PORT, status.port ?: -1)
                putExtra(EXTRA_ADB_MESSAGE, status.message)
            }
            ContextCompat.startForegroundService(context, intent)
        }
    }
}
