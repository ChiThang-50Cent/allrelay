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
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

class AllRelayService : Service() {
    private val serverExecutor = Executors.newSingleThreadExecutor()
    private val clientExecutor = Executors.newCachedThreadPool()
    private val scheduler = Executors.newSingleThreadScheduledExecutor()
    private val adbLock = Any()

    @Volatile
    private var serverSocket: ServerSocket? = null
    private var autoDisableFuture: ScheduledFuture<*>? = null
    private var autoDisableAtMs: Long = 0
    private var autoDisablePort: Int = RootDaemonManager.ADB_TCP_DEFAULT_PORT
    private var adbIdleStartMs: Long = 0L

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
        startForeground(NOTIFICATION_ID, buildNotification())
        restoreAdbTimeoutIfNeeded()
        startControlServer()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
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
                    clientExecutor.execute {
                        handleClient(client)
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

            method == "GET" && path == "/adb/status" -> 200 to buildAdbStatusJson()

            method == "POST" && path == "/adb/enable" -> {
                val req = if (body.isBlank()) JSONObject() else JSONObject(body)
                val port = req.optInt("port", RootDaemonManager.ADB_TCP_DEFAULT_PORT)
                val timeoutSeconds = req.optInt("timeoutSeconds", DEFAULT_ADB_TIMEOUT_SECONDS)
                val status = RootDaemonManager.enableWirelessAdb(port)
                if (!status.enabled) {
                    500 to buildAdbStatusJson(status).put("message", status.message)
                } else {
                    scheduleAutoDisable(port, timeoutSeconds)
                    200 to buildAdbStatusJson(status)
                }
            }

            method == "POST" && path == "/adb/disable" -> {
                cancelAutoDisable()
                val status = RootDaemonManager.disableWirelessAdb()
                200 to buildAdbStatusJson(status)
            }

            method == "POST" && path == "/adb/authorize" -> {
                val req = if (body.isBlank()) JSONObject() else JSONObject(body)
                val key = req.optString("key", "").trim()
                if (key.isBlank()) {
                    400 to jsonError("Missing 'key' field")
                } else {
                    val ok = RootDaemonManager.authorizeAdbKey(key)
                    if (ok) {
                        200 to JSONObject().put("ok", true).put("message", "Host key authorized")
                    } else {
                        500 to jsonError("Failed to authorize host key (root may be denied)")
                    }
                }
            }

            else -> 404 to jsonError("Not found")
        }
    }

    private fun buildAdbStatusJson(status: RootDaemonManager.WirelessAdbStatus = RootDaemonManager.wirelessAdbStatus()): JSONObject {
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

    private fun scheduleAutoDisable(port: Int, timeoutSeconds: Int) {
        val safeTimeout = timeoutSeconds.coerceAtLeast(30)
        synchronized(adbLock) {
            autoDisableFuture?.cancel(false)
            autoDisablePort = port
            adbIdleStartMs = System.currentTimeMillis()
            autoDisableAtMs = System.currentTimeMillis() + safeTimeout * 1000L
            persistAdbTimer(autoDisableAtMs, autoDisablePort)
            autoDisableFuture = scheduler.scheduleWithFixedDelay({
                checkAdbIdleTimeout(port, safeTimeout)
            }, 30, 30, TimeUnit.SECONDS)
        }
    }

    private fun checkAdbIdleTimeout(port: Int, timeoutSeconds: Int) {
        val hasHost = RootDaemonManager.hasAdbHostConnected(port)
        val now = System.currentTimeMillis()
        synchronized(adbLock) {
            if (hasHost) {
                adbIdleStartMs = now
                autoDisableAtMs = now + timeoutSeconds * 1000L
                persistAdbTimer(autoDisableAtMs, autoDisablePort)
                return
            }
            val idleMs = now - adbIdleStartMs
            if (idleMs >= timeoutSeconds * 1000L) {
                autoDisableFuture?.cancel(false)
                autoDisableFuture = null
                autoDisableAtMs = 0L
                clearAdbTimer()
                try {
                    RootDaemonManager.disableWirelessAdb()
                } catch (_: Exception) {}
            }
        }
    }

    private fun cancelAutoDisable() {
        synchronized(adbLock) {
            autoDisableFuture?.cancel(false)
            autoDisableFuture = null
            autoDisableAtMs = 0L
            adbIdleStartMs = 0L
            clearAdbTimer()
        }
    }

    private fun restoreAdbTimeoutIfNeeded() {
        val prefs = getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val disableAt = prefs.getLong(PREF_ADB_DISABLE_AT_MS, 0L)
        val port = prefs.getInt(PREF_ADB_PORT, RootDaemonManager.ADB_TCP_DEFAULT_PORT)
        if (disableAt <= 0L) {
            return
        }

        val now = System.currentTimeMillis()
        val remainingMs = disableAt - now
        if (remainingMs <= 0L) {
            RootDaemonManager.disableWirelessAdb()
            clearAdbTimer()
            return
        }

        val status = RootDaemonManager.wirelessAdbStatus()
        if (!status.enabled) {
            clearAdbTimer()
            return
        }

        synchronized(adbLock) {
            autoDisablePort = port
            autoDisableAtMs = disableAt
            adbIdleStartMs = disableAt - DEFAULT_ADB_TIMEOUT_SECONDS * 1000L
            autoDisableFuture = scheduler.scheduleWithFixedDelay({
                checkAdbIdleTimeout(port, DEFAULT_ADB_TIMEOUT_SECONDS)
            }, 30, 30, TimeUnit.SECONDS)
        }
    }

    private fun persistAdbTimer(disableAtMs: Long, port: Int) {
        getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .edit()
            .putLong(PREF_ADB_DISABLE_AT_MS, disableAtMs)
            .putInt(PREF_ADB_PORT, port)
            .apply()
    }

    private fun clearAdbTimer() {
        synchronized(adbLock) {
            autoDisableAtMs = 0L
            getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
                .edit()
                .remove(PREF_ADB_DISABLE_AT_MS)
                .remove(PREF_ADB_PORT)
                .apply()
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
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return
        }
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

        private const val NOTIFICATION_ID = 4001
        private const val CHANNEL_ID = "allrelay-background"
        private const val PREFS_NAME = "allrelay_service"
        private const val PREF_ADB_DISABLE_AT_MS = "adb_disable_at_ms"
        private const val PREF_ADB_PORT = "adb_port"

        fun ensureRunning(context: Context) {
            val intent = Intent(context, AllRelayService::class.java)
            ContextCompat.startForegroundService(context, intent)
        }
    }
}
