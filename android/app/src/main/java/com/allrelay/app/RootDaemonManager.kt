package com.allrelay.app

import android.content.Context
import android.util.Log
import java.io.File
import java.util.concurrent.TimeUnit

object RootDaemonManager {
    private const val SERVER_ASSET_NAME = "allrelay.jar"
    private const val ROOT_LOG_PATH = "/data/local/tmp/allrelay-unified.log"
    private const val LOCAL_TMP_JAR = "/data/local/tmp/allrelay.jar"
    private const val MAGISK_JAR = "/data/adb/modules/allrelay/system/bin/scrcpy-server-allrelay.jar"
    private const val PROCESS_PATTERN = "com.genymobile.scrcpy.Server"
    private const val CAMERA_SAFE_SIZE = "1280x720"
    private const val CAMERA_SAFE_FPS = 15
    private const val CAMERA_SAFE_VIDEO_BIT_RATE = 3_000_000
    const val ADB_TCP_DEFAULT_PORT = 5555

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
        val cameraTuningArgs = buildCameraTuningArgs(config)

        val startScript = "/data/local/tmp/allrelay-start.sh"
        val script = """
#!/system/bin/sh
# Kill existing instances
pkill -9 -f 'app_process.*com.genymobile.scrcpy.Server' >/dev/null 2>&1 || true
pkill -9 -f 'app_process.*Server' >/dev/null 2>&1 || true
pkill -9 -f 'CLASSPATH.*scrcpy' >/dev/null 2>&1 || true
sleep 2

# Verify ports released
if [ "\$(ss -tln 2>/dev/null | grep -E ':5000|:5001|:5002|:5003|:5004' | wc -l)" -gt 0 ]; then
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
${cameraTuningArgs}  daemon=true \
  control=${config.screen} \
  power_on=false \
  keep_active=${config.screen} \
  > '$ROOT_LOG_PATH' 2>&1 &
""".trimIndent()

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

    fun authorizeAdbKey(key: String): Boolean {
        val cleanKey = key.trim().replace("\r", "").replace("\n", "")
        if (cleanKey.isBlank()) return false
        val result = runSu(
            "mkdir -p /data/misc/adb && chmod 700 /data/misc/adb && echo '$cleanKey' >> /data/misc/adb/adb_keys && chmod 600 /data/misc/adb/adb_keys && chown system:shell /data/misc/adb/adb_keys",
            timeoutSeconds = 10,
        )
        return result.exitCode == 0
    }

    fun hasAdbHostConnected(port: Int = ADB_TCP_DEFAULT_PORT): Boolean {
        val result = runSu(
            "ss -tn 2>/dev/null | grep ':$port ' | grep -iq estab && echo yes || echo no",
            timeoutSeconds = 5,
        )
        return result.exitCode == 0 && result.stdout.trim() == "yes"
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

    fun status(config: Config? = null): Status {
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

        val expected = config?.expectedPorts().orEmpty()
        val hasDaemonPorts = ports.intersect(setOf(5000, 5001, 5002, 5003, 5004)).isNotEmpty()
        val message = when {
            !hasRoot() -> "Root unavailable"
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
        copyBundledJarIfPresent(context)?.let { return it }
        if (fileExistsViaRoot(MAGISK_JAR)) {
            return MAGISK_JAR
        }
        if (fileExistsViaRoot(LOCAL_TMP_JAR)) {
            return LOCAL_TMP_JAR
        }
        error("No bundled allrelay.jar found. Install app with bundled server or keep Magisk module installed.")
    }

    private fun copyBundledJarIfPresent(context: Context): String? {
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
        } catch (_: Exception) {
            null
        }
    }

    private fun fileExistsViaRoot(path: String): Boolean {
        val result = runSu("[ -r '$path' ] && echo yes || echo no", timeoutSeconds = 5)
        return result.exitCode == 0 && result.stdout.trim() == "yes"
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
