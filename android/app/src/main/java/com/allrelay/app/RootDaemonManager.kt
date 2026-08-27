package com.allrelay.app

import android.content.Context
import android.util.Log
import java.io.File
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.nio.charset.StandardCharsets
import java.util.Base64
import java.util.concurrent.TimeUnit

object RootDaemonManager {
    private const val SERVER_ASSET_NAME = "allrelay.jar"
    private const val ROOT_LOG_PATH = "/data/local/tmp/allrelay-unified.log"
    private const val PROCESS_PATTERN = "com.genymobile.scrcpy.Server"
    private const val CAMERA_SAFE_SIZE = "1280x720"
    private const val CAMERA_SAFE_FPS = 15
    private const val CAMERA_SAFE_VIDEO_BIT_RATE = 3_000_000
    private const val SCREEN_VIDEO_BIT_RATE = 16_000_000
    const val ADB_TCP_DEFAULT_PORT = 5555
    private const val DISCOVERY_PORT = 5009
    private const val DISCOVERY_TIMEOUT_MS = 500

    private const val ADB_KEYS_DIRECTORY = "/data/misc/adb"
    private const val ADB_KEYS_PATH = "$ADB_KEYS_DIRECTORY/adb_keys"
    private const val ADB_PUBLIC_KEY_ENCODED_LENGTH = 700
    private const val ADB_PUBLIC_KEY_DECODED_LENGTH = 524

    data class Config(
        val screen: Boolean,
        val camera: Boolean,
        val mic: Boolean,
        val speaker: Boolean,
    ) {
        fun validate() {
            require(screen || camera || mic || speaker) { "Enable at least one stream" }
        }

        fun expectedPorts(): Set<Int> = buildSet {
            if (screen) {
                add(5000)
                add(5004)
            }
            if (camera) add(5001)
            if (mic) add(5002)
            if (speaker) add(5003)
        }
    }

    data class Status(
        val running: Boolean,
        val pid: String?,
        val ports: Set<Int>,
        val message: String,
        val logTail: String,
    )

    data class CommandResult(
        val exitCode: Int,
        val stdout: String,
        val stderr: String,
    )

    data class WirelessAdbStatus(
        val enabled: Boolean,
        val listening: Boolean,
        val port: Int?,
        val message: String,
    )

    fun hasRoot(): Boolean {
        val result = runSu("id -u", timeoutSeconds = 5)
        return result.exitCode == 0 && result.stdout.trim() == "0"
    }

    fun start(context: Context, config: Config): Status {
        config.validate()

        val existing = status(config)
        if (existing.running && existing.ports.containsAll(config.expectedPorts())) {
            return existing.copy(message = "Daemon already running")
        }

        val jarPath = resolveJarPath(context)
        val startScript = "/data/local/tmp/allrelay-start.sh"
        val script = buildStartScript(jarPath, config)

        // Write script via stdin to avoid quoting issues
        try {
            val writeProcess = ProcessBuilder("su").start()
            writeProcess.outputStream.bufferedWriter().use { writer ->
                writer.write("cat > '$startScript' << 'EOF'")
                writer.newLine()
                writer.write(script)
                writer.newLine()
                writer.write("EOF")
                writer.newLine()
                writer.write("chmod 755 '$startScript'")
                writer.newLine()
            }
            val writeFinished = writeProcess.waitFor(10, TimeUnit.SECONDS)
            if (!writeFinished) {
                writeProcess.destroyForcibly()
                Log.e("AllRelay", "Write script timed out")
            }
        } catch (e: Exception) {
            Log.e("AllRelay", "Write script failed: $e")
        }

        val result = runSu("sh '$startScript'", timeoutSeconds = 15)
        if (result.exitCode != 0) {
            return Status(
                running = false,
                pid = null,
                ports = emptySet(),
                message = "Start failed: ${result.stderr.ifBlank { result.stdout }.trim()}",
                logTail = readLogTail(),
            )
        }

        repeat(10) {
            Thread.sleep(1000)
            val status = status(config)
            if (status.running && status.ports.containsAll(config.expectedPorts())) {
                return status.copy(message = "Daemon running")
            }
        }

        return status(config).copy(message = "Daemon started but health check incomplete")
    }

    fun restart(context: Context, config: Config): Status {
        stop()
        Thread.sleep(1000)
        return start(context, config)
    }

    fun stop(): Status {
        val result = runSu("pkill -9 -f '$PROCESS_PATTERN' >/dev/null 2>&1 || true", timeoutSeconds = 10)
        val base = status(null)
        val message = if (result.exitCode == 0) "Daemon stopped" else "Stop may have failed"
        return base.copy(message = message)
    }

    fun enableWirelessAdb(port: Int = ADB_TCP_DEFAULT_PORT): WirelessAdbStatus {
        val safePort = if (port in 1..65535) port else ADB_TCP_DEFAULT_PORT
        val result = runSu(
            "setprop service.adb.tcp.port $safePort; stop adbd >/dev/null 2>&1 || true; start adbd >/dev/null 2>&1 || true; setprop ctl.restart adbd >/dev/null 2>&1 || true; sleep 1",
            timeoutSeconds = 10,
        )
        repeat(8) {
            val status = wirelessAdbStatus(safePort)
            if (status.listening) {
                return status.copy(message = "ADB over Wi‑Fi enabled on port $safePort")
            }
            Thread.sleep(500)
        }
        val status = wirelessAdbStatus(safePort)
        if (status.enabled) {
            return status.copy(message = status.message)
        }
        val reason = result.stderr.ifBlank { result.stdout }.trim().ifBlank { status.message }
        return status.copy(message = reason)
    }

    fun disableWirelessAdb(): WirelessAdbStatus {
        val result = runSu(
            "setprop service.adb.tcp.port 0; stop adbd >/dev/null 2>&1 || true; start adbd >/dev/null 2>&1 || true; setprop ctl.restart adbd >/dev/null 2>&1 || true; sleep 1",
            timeoutSeconds = 10,
        )
        repeat(8) {
            val status = wirelessAdbStatus()
            if (!status.enabled && !status.listening) {
                return status.copy(message = "ADB over Wi‑Fi disabled")
            }
            Thread.sleep(500)
        }
        val status = wirelessAdbStatus()
        if (!status.enabled) {
            return status.copy(message = "ADB over Wi‑Fi disabled")
        }
        val reason = result.stderr.ifBlank { result.stdout }.trim().ifBlank { status.message }
        return status.copy(message = reason)
    }

    /**
     * Adds a host key through a complete replacement rather than append-only writes.
     *
     * ADB reads this file as a line-oriented set. Rebuilding it removes malformed
     * records left by interrupted requests, deduplicates the requested host key,
     * and lets the rename publish a complete file to adbd.
     */
    private val adbKeyAuthorizationLock = Any()

    fun authorizeAdbKey(key: String): Boolean = synchronized(adbKeyAuthorizationLock) {
        val requested = normalizeAdbKeyLine(key) ?: return@synchronized false
        val existing = runSu(
            "if [ -f '$ADB_KEYS_PATH' ]; then cat '$ADB_KEYS_PATH'; fi",
            timeoutSeconds = 10,
        )
        if (existing.exitCode != 0) {
            return@synchronized false
        }

        val keys = mergeAdbKeyLines(existing.stdout.lineSequence(), requested) ?: return@synchronized false
        replaceAdbKeyFile(keys)
    }

    internal fun normalizeAdbKeyLine(line: String): String? {
        if (line.any { it == '\r' || it == '\n' }) {
            return null
        }
        val trimmed = line.trim()
        if (trimmed.isEmpty() || trimmed.length > 2_048) {
            return null
        }

        val fields = trimmed.split(Regex("\\s+"), limit = 2)
        val material = fields.firstOrNull() ?: return null
        if (material.length != ADB_PUBLIC_KEY_ENCODED_LENGTH ||
            !material.endsWith('=') ||
            !material.dropLast(1).matches(Regex("[A-Za-z0-9+/]+"))
        ) {
            return null
        }

        val decoded = try {
            Base64.getDecoder().decode(material)
        } catch (_: IllegalArgumentException) {
            return null
        }
        // adbkey.pub stores a serialized 2048-bit RSAPublicKey, whose first
        // little-endian word is the 64-word modulus length.
        if (decoded.size != ADB_PUBLIC_KEY_DECODED_LENGTH ||
            (decoded[0].toInt() and 0xff) != 64 ||
            decoded[1].toInt() != 0 ||
            decoded[2].toInt() != 0 ||
            decoded[3].toInt() != 0
        ) {
            return null
        }

        val comment = fields.getOrNull(1)?.trim()
        if (comment != null && (comment.length > 1_024 || comment.any { it.code !in 0x20..0x7e })) {
            return null
        }
        return if (comment.isNullOrEmpty()) material else "$material $comment"
    }

    internal fun mergeAdbKeyLines(existing: Sequence<String>, requested: String): List<String>? {
        val normalizedRequested = normalizeAdbKeyLine(requested) ?: return null
        val keysByMaterial = LinkedHashMap<String, String>()
        existing.forEach { line ->
            val normalized = normalizeAdbKeyLine(line) ?: return@forEach
            keysByMaterial.putIfAbsent(normalized.substringBefore(' '), normalized)
        }

        val requestedMaterial = normalizedRequested.substringBefore(' ')
        keysByMaterial.remove(requestedMaterial)
        keysByMaterial[requestedMaterial] = normalizedRequested
        return keysByMaterial.values.toList()
    }

    private fun replaceAdbKeyFile(keys: List<String>): Boolean {
        val encodedKeys = Base64.getEncoder().encodeToString(
            keys.joinToString(separator = "\n", postfix = "\n").toByteArray(StandardCharsets.UTF_8),
        )
        val result = runSu(
            """
            set -eu
            dir='$ADB_KEYS_DIRECTORY'
            keys="${'$'}dir/adb_keys"
            tmp="${'$'}dir/.adb_keys.allrelay.${'$'}${'$'}"
            trap 'rm -f "${'$'}tmp"' EXIT
            mkdir -p "${'$'}dir"
            chown system:shell "${'$'}dir"
            chmod 2750 "${'$'}dir"
            printf '%s' '$encodedKeys' | base64 -d > "${'$'}tmp"
            chown system:shell "${'$'}tmp"
            chmod 0640 "${'$'}tmp"
            mv -f "${'$'}tmp" "${'$'}keys"
            chown system:shell "${'$'}keys"
            chmod 0640 "${'$'}keys"
            restorecon "${'$'}dir" "${'$'}keys"
            setprop ctl.restart adbd
            """.trimIndent(),
            timeoutSeconds = 10,
        )
        return result.exitCode == 0
    }


    fun wirelessAdbStatus(expectedPort: Int? = null): WirelessAdbStatus {
        val propResult = runSu("getprop service.adb.tcp.port", timeoutSeconds = 5)
        val propPort = propResult.stdout.trim().toIntOrNull()
        val port = when {
            expectedPort != null && expectedPort > 0 -> expectedPort
            propPort != null && propPort > 0 -> propPort
            else -> ADB_TCP_DEFAULT_PORT
        }
        val listeningResult = runSu(
            "ss -tln 2>/dev/null | grep -q ':$port ' && echo yes || echo no",
            timeoutSeconds = 5,
        )
        val listening = listeningResult.exitCode == 0 && listeningResult.stdout.trim() == "yes"
        val enabled = (propPort != null && propPort > 0) || listening
        val message = when {
            enabled && listening -> "ADB TCP listening on port $port"
            enabled -> "ADB TCP requested on port $port, waiting for listener"
            else -> "ADB over Wi‑Fi disabled"
        }
        return WirelessAdbStatus(
            enabled = enabled,
            listening = listening,
            port = if (enabled || listening) port else null,
            message = message,
        )
    }

    fun status(config: Config? = null, rooted: Boolean? = null): Status {
        val pidResult = runSu(
            "ps -A -o PID,ARGS | grep -E 'app_process.*com.genymobile.scrcpy.Server' | grep -v grep | awk 'NR==1{print \$1}'",
            timeoutSeconds = 5
        )
        val pid = pidResult.stdout.trim().takeIf { it.isNotEmpty() }
        val running = pid != null

        val portsResult = runSu(
            "for p in 5000 5001 5002 5003 5004; do ss -tln 2>/dev/null | grep -q \":\$p\" && echo PORT:\$p; done",
            timeoutSeconds = 5
        )
        val ports = portsResult.stdout.lineSequence()
            .filter { it.startsWith("PORT:") }
            .mapNotNull { it.substringAfter(":").trim().toIntOrNull() }
            .toSet()

        val rootAvailable = rooted ?: hasRoot()
        val expected = config?.expectedPorts().orEmpty()
        val hasDaemonPorts = ports.intersect(setOf(5000, 5001, 5002, 5003, 5004)).isNotEmpty()
        val message = when {
            !rootAvailable -> "Root unavailable"
            running && expected.isNotEmpty() && !ports.containsAll(expected) -> "Process up, waiting for ports ${expected.sorted()}"
            running -> "Daemon running${pid?.let { " (pid $it)" } ?: ""}"
            hasDaemonPorts -> "Daemon ports detected (process lookup failed)"
            else -> "Daemon stopped"
        }

        val effectiveRunning = running || hasDaemonPorts

        return Status(
            running = effectiveRunning,
            pid = pid,
            ports = ports,
            message = message,
            logTail = readLogTail(),
        )
    }

    // The daemon's UDP discovery responder is readable by the app UID, unlike
    // process and socket tables. Use it for automatic UI refreshes so they do
    // not invoke su or trigger a root notification.
    fun statusWithoutRoot(): Status {
        val query = "{\"query\":\"allrelay\"}".toByteArray(StandardCharsets.UTF_8)
        val response = ByteArray(256)
        val daemonReachable = try {
            DatagramSocket().use { socket ->
                socket.soTimeout = DISCOVERY_TIMEOUT_MS
                socket.send(DatagramPacket(query, query.size, InetAddress.getLoopbackAddress(), DISCOVERY_PORT))
                val packet = DatagramPacket(response, response.size)
                socket.receive(packet)
                String(packet.data, 0, packet.length, StandardCharsets.UTF_8)
                    .contains("\"name\":\"AllRelay\"")
            }
        } catch (_: Exception) {
            false
        }

        return Status(
            running = daemonReachable,
            pid = null,
            ports = emptySet(),
            message = if (daemonReachable) {
                "Daemon reachable via local discovery; tap Refresh for PID, ports, and logs"
            } else {
                "Discovery did not confirm the daemon; tap Refresh for root-verified status"
            },
            logTail = "",
        )
    }

    internal fun buildStartScript(jarPath: String, config: Config): String {
        val screenTuningArgs = buildScreenTuningArgs(config)
        val cameraTuningArgs = buildCameraTuningArgs(config)
        return """
#!/system/bin/sh
# Kill existing instances
pkill -9 -f 'app_process.*com.genymobile.scrcpy.Server' >/dev/null 2>&1 || true
pkill -9 -f 'app_process.*Server' >/dev/null 2>&1 || true
pkill -9 -f 'CLASSPATH.*scrcpy' >/dev/null 2>&1 || true
sleep 2

if [ "${'$'}(ss -tln 2>/dev/null | grep -E ':5000|:5001|:5002|:5003|:5004' | wc -l)" -gt 0 ]; then
  echo 'WARNING: ports still in use after kill' >> '$ROOT_LOG_PATH'
  fuser -k 5000/tcp 5001/tcp 5002/tcp 5003/tcp 5004/tcp 2>/dev/null || true
fi

rm -f '$ROOT_LOG_PATH'

# Start daemon
# power_on=false: do not wake the phone just because the control channel connects
# keep_active=true: match scrcpy keep-active semantics while screen remote mode is enabled
CLASSPATH=$jarPath exec app_process / com.genymobile.scrcpy.Server \
  4.0 \
  log_level=info \
  video=${config.screen} \
  audio=${config.mic} \
  audio_source=mic \
  wifi_mode=true \
  wifi_port=5000 \
  speaker_enabled=${config.speaker} \
  camera_enabled=${config.camera} \
${screenTuningArgs}${cameraTuningArgs}  daemon=true \
  control=${config.screen} \
  power_on=false \
  keep_active=${config.screen} \
  > '$ROOT_LOG_PATH' 2>&1 &
""".trimIndent()
    }

    private fun buildScreenTuningArgs(config: Config): String {
        if (!config.screen) {
            return ""
        }

        return buildString {
            append("  screen_video_bit_rate=")
            append(SCREEN_VIDEO_BIT_RATE)
            append(" \\\n")
        }
    }

    private fun buildCameraTuningArgs(config: Config): String {
        if (!config.camera) {
            return ""
        }

        // Camera profile tuned for stability on thermally constrained phones.
        // camera_size/camera_fps only affect the camera path.
        // video_bit_rate/max_fps are global scrcpy options, so only apply them
        // in camera-only mode to avoid unexpectedly degrading screen mirroring.
        return buildString {
            append("  camera_size=")
            append(CAMERA_SAFE_SIZE)
            append(" \\\n")
            append("  camera_fps=")
            append(CAMERA_SAFE_FPS)
            append(" \\\n")
            if (!config.screen) {
                append("  video_bit_rate=")
                append(CAMERA_SAFE_VIDEO_BIT_RATE)
                append(" \\\n")
                append("  max_fps=")
                append(CAMERA_SAFE_FPS)
                append(" \\\n")
            }
        }
    }

    private fun resolveJarPath(context: Context): String {
        return try {
            context.assets.open(SERVER_ASSET_NAME).use { input ->
                val dir = File(context.filesDir, "allrelay")
                if (!dir.exists()) {
                    dir.mkdirs()
                }
                val outFile = File(dir, SERVER_ASSET_NAME)
                input.copyTo(outFile.outputStream())
                outFile.setReadable(true, false)
                outFile.absolutePath
            }
        } catch (e: Exception) {
            error("Bundled allrelay.jar is unavailable: ${e.message ?: e.javaClass.simpleName}")
        }
    }

    private fun readLogTail(): String {
        val result = runSu("tail -n 80 '$ROOT_LOG_PATH' 2>/dev/null || true", timeoutSeconds = 5)
        val text = (result.stdout + result.stderr).trim()
        return if (text.isBlank()) "<no log yet>" else text
    }

    private fun runSu(command: String, timeoutSeconds: Long, useStdin: Boolean = false): CommandResult {
        return try {
            val process = if (useStdin) {
                ProcessBuilder("su").start().apply {
                    outputStream.bufferedWriter().use { writer ->
                        writer.write(command)
                        writer.newLine()
                        writer.write("exit")
                        writer.newLine()
                    }
                }
            } else {
                ProcessBuilder("su", "-c", command).start()
            }

            val finished = process.waitFor(timeoutSeconds, TimeUnit.SECONDS)
            if (!finished) {
                process.destroyForcibly()
                return CommandResult(-1, "", "Timed out")
            }

            val stdout = process.inputStream.bufferedReader().readText()
            val stderr = process.errorStream.bufferedReader().readText()
            CommandResult(process.exitValue(), stdout, stderr)
        } catch (e: Exception) {
            Log.e("AllRelay", "runSu exception: $e")
            CommandResult(-1, "", e.message ?: e.javaClass.simpleName)
        }
    }
}
