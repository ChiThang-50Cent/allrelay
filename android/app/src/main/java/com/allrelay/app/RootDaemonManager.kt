package com.allrelay.app

import android.content.Context
import java.io.File
import java.util.concurrent.TimeUnit

object RootDaemonManager {
    private const val SERVER_ASSET_NAME = "allrelay.jar"
    private const val ROOT_LOG_PATH = "/data/local/tmp/allrelay-unified.log"
    private const val LOCAL_TMP_JAR = "/data/local/tmp/allrelay.jar"
    private const val MAGISK_JAR = "/data/adb/modules/allrelay/system/bin/scrcpy-server-allrelay.jar"
    private const val PROCESS_PATTERN = "com.genymobile.scrcpy.Server"

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

    fun hasRoot(): Boolean {
        val result = runSu("id -u", timeoutSeconds = 5)
        return result.exitCode == 0 && result.stdout.trim() == "0"
    }

    fun start(context: Context, config: Config): Status {
        config.validate()
        val jarPath = resolveJarPath(context)
        val command = buildString {
            appendLine("pkill -9 -f '$PROCESS_PATTERN' >/dev/null 2>&1 || true")
            appendLine("rm -f '$ROOT_LOG_PATH'")
            append("nohup sh -c 'CLASSPATH=$jarPath exec app_process / com.genymobile.scrcpy.Server 4.0")
            append(" log_level=info")
            append(" video=${config.screen}")
            append(" audio=${config.mic}")
            append(" audio_source=mic")
            append(" wifi_mode=true")
            append(" wifi_port=5000")
            append(" speaker_enabled=${config.speaker}")
            append(" camera_enabled=${config.camera}")
            append(" daemon=true")
            append(" control=${config.screen}")
            append("' >'$ROOT_LOG_PATH' 2>&1 </dev/null &")
        }

        val result = runSu(command, timeoutSeconds = 15)
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

    fun status(config: Config? = null): Status {
        val result = runSu(
            """
            PID="$(ps -A -o PID,PPID,NAME,ARGS | grep '$PROCESS_PATTERN' | grep -v grep | awk 'NR==1 {print \$1}')"
            if [ -z "${'$'}PID" ]; then
              PID="$(for f in /proc/[0-9]*/cmdline; do tr '\000' ' ' < "${'$'}f" 2>/dev/null | grep -q '$PROCESS_PATTERN' && basename "$(dirname "${'$'}f")" && break; done)"
            fi
            if [ -n "${'$'}PID" ]; then
              echo "RUNNING:${'$'}PID"
            else
              echo "STOPPED"
            fi
            ss -tln 2>/dev/null | awk 'NR>1 {print ${'$'}4}' | sed 's/.*://' | sort -u | while read -r p; do
              [ -n "${'$'}p" ] && echo "PORT:${'$'}p"
            done
            """.trimIndent(),
            timeoutSeconds = 10,
        )

        val pid = result.stdout.lineSequence()
            .firstOrNull { it.startsWith("RUNNING:") }
            ?.substringAfter(':')
            ?.trim()
            ?.takeIf { it.isNotEmpty() }
        val running = pid != null
        val ports = result.stdout.lineSequence()
            .filter { it.startsWith("PORT:") }
            .mapNotNull { it.substringAfter(':').trim().toIntOrNull() }
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

    private fun runSu(command: String, timeoutSeconds: Long): CommandResult {
        return try {
            val process = ProcessBuilder("su").start()
            process.outputStream.bufferedWriter().use { writer ->
                writer.write(command)
                writer.newLine()
                writer.write("exit")
                writer.newLine()
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
            CommandResult(-1, "", e.message ?: e.javaClass.simpleName)
        }
    }
}
