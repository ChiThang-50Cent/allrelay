package com.allrelay.app

import android.app.Activity
import android.graphics.Color
import android.graphics.Typeface
import android.net.wifi.WifiManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.TypedValue
import android.view.Gravity
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView
import java.util.concurrent.Executors

class ToggleActivity : Activity() {
    private val executor = Executors.newSingleThreadExecutor()
    private val mainHandler = Handler(Looper.getMainLooper())
    private var isTransitioning = false

    private lateinit var statusText: TextView
    private lateinit var ipText: TextView
    private lateinit var rootText: TextView
    private lateinit var portsText: TextView
    private lateinit var logText: TextView
    private lateinit var screenSwitch: Switch
    private lateinit var cameraSwitch: Switch
    private lateinit var micSwitch: Switch
    private lateinit var speakerSwitch: Switch
    private lateinit var startButton: Button
    private lateinit var restartButton: Button
    private lateinit var stopButton: Button
    private lateinit var refreshButton: Button
    private lateinit var adbStatusText: TextView
    private lateinit var adbEnableButton: Button
    private lateinit var adbDisableButton: Button
    private lateinit var adbRefreshButton: Button

    private val refreshRunnable = object : Runnable {
        override fun run() {
            refreshStatus(showBusy = false)
            refreshAdbStatus(showBusy = false)
            mainHandler.postDelayed(this, 2500)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        AllRelayService.ensureRunning(this)
        setContentView(createLayout())
        updateIp()
        rootText.text = "Root: checking..."
        refreshStatus(showBusy = false)
        refreshAdbStatus(showBusy = false)
    }

    override fun onResume() {
        super.onResume()
        mainHandler.post(refreshRunnable)
    }

    override fun onPause() {
        super.onPause()
        mainHandler.removeCallbacks(refreshRunnable)
    }

    override fun onDestroy() {
        super.onDestroy()
        executor.shutdownNow()
    }

    private fun createLayout(): ScrollView {
        val scroll = ScrollView(this).apply {
            setBackgroundColor(Color.parseColor("#101826"))
        }

        val layout = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(48, 48, 48, 48)
        }
        scroll.addView(layout)

        layout.addView(TextView(this).apply {
            text = "AllRelay"
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 30f)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(0, 0, 0, 12)
        })

        layout.addView(TextView(this).apply {
            text = "Tap once to start Android daemon for Wi‑Fi discovery"
            setTextColor(Color.parseColor("#9fb3c8"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(0, 0, 0, 24)
        })

        statusText = infoLine(layout, "Status", "Ready")
        rootText = infoLine(layout, "Root", "Unknown")
        ipText = infoLine(layout, "Phone", "Detecting Wi‑Fi IP...")
        portsText = infoLine(layout, "Ports", "-")

        layout.addView(sectionTitle("Streams"))
        screenSwitch = createStreamToggle("Screen + control daemons (:5000, :5004)", true)
        cameraSwitch = createStreamToggle("Camera daemon (:5001)", true)
        micSwitch = createStreamToggle("Mic daemon (:5002)", true)
        speakerSwitch = createStreamToggle("Speaker daemon (:5003)", true)
        layout.addView(screenSwitch)
        layout.addView(cameraSwitch)
        layout.addView(micSwitch)
        layout.addView(speakerSwitch)

        val row1 = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(0, 36, 0, 12)
        }
        startButton = actionButton("Start") { startDaemon() }
        restartButton = actionButton("Restart") { restartDaemon() }
        row1.addView(startButton)
        row1.addView(restartButton)
        layout.addView(row1)

        val row2 = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_HORIZONTAL
        }
        stopButton = actionButton("Stop") { stopDaemon() }
        refreshButton = actionButton("Refresh") { refreshStatus() }
        row2.addView(stopButton)
        row2.addView(refreshButton)
        layout.addView(row2)

        layout.addView(sectionTitle("Daemon log"))
        logText = TextView(this).apply {
            setTextColor(Color.parseColor("#b8c7d9"))
            setBackgroundColor(Color.parseColor("#0b1220"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
            typeface = Typeface.MONOSPACE
            setPadding(24, 24, 24, 24)
            text = "<loading>"
        }
        layout.addView(logText)

        layout.addView(sectionTitle("Wireless ADB"))
        adbStatusText = infoLine(layout, "ADB", "Checking...")
        val adbRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(0, 12, 0, 12)
        }
        adbEnableButton = actionButton("Enable ADB") { enableAdb() }
        adbDisableButton = actionButton("Disable ADB") { disableAdb() }
        adbRefreshButton = actionButton("Refresh") { refreshAdbStatus() }
        adbRow.addView(adbEnableButton)
        adbRow.addView(adbDisableButton)
        adbRow.addView(adbRefreshButton)
        layout.addView(adbRow)

        return scroll
    }

    private fun infoLine(parent: LinearLayout, label: String, initial: String): TextView {
        val view = TextView(this).apply {
            text = "$label: $initial"
            setTextColor(Color.parseColor("#dbe7f3"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            setPadding(0, 0, 0, 10)
        }
        parent.addView(view)
        return view
    }

    private fun sectionTitle(text: String): TextView {
        return TextView(this).apply {
            this.text = text
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 20f)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(0, 28, 0, 14)
        }
    }

    private fun createStreamToggle(text: String, checked: Boolean): Switch {
        return Switch(this).apply {
            this.text = text
            isChecked = checked
            setTextColor(Color.parseColor("#e6eef8"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
            setPadding(18, 24, 18, 24)
            setBackgroundColor(Color.parseColor("#162235"))
            val params = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            )
            params.setMargins(0, 0, 0, 12)
            layoutParams = params
        }
    }

    private fun actionButton(label: String, onClick: () -> Unit): Button {
        return Button(this).apply {
            text = label
            setOnClickListener { onClick() }
            setTextColor(Color.WHITE)
            setBackgroundColor(Color.parseColor("#e94560"))
            val params = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
            params.setMargins(10, 0, 10, 0)
            layoutParams = params
        }
    }

    private fun currentConfig(): RootDaemonManager.Config {
        return RootDaemonManager.Config(
            screen = screenSwitch.isChecked,
            camera = cameraSwitch.isChecked,
            mic = micSwitch.isChecked,
            speaker = speakerSwitch.isChecked,
        )
    }

    private fun startDaemon() {
        runAction("Starting daemon...") {
            RootDaemonManager.start(this, currentConfig())
        }
    }

    private fun restartDaemon() {
        runAction("Restarting daemon...") {
            RootDaemonManager.restart(this, currentConfig())
        }
    }

    private fun stopDaemon() {
        runAction("Stopping daemon...") {
            RootDaemonManager.stop()
        }
    }

    private fun refreshStatus(showBusy: Boolean = true) {
        if (isTransitioning) {
            return
        }
        if (showBusy) {
            statusText.text = "Status: Refreshing..."
        }
        executor.execute {
            val rooted = RootDaemonManager.hasRoot()
            val status = RootDaemonManager.status(currentConfig())
            mainHandler.post {
                if (isTransitioning) return@post
                rootText.text = "Root: ${if (rooted) "OK" else "Unavailable"}"
                renderStatus(status)
            }
        }
    }

    private fun runAction(busyText: String, action: () -> RootDaemonManager.Status) {
        isTransitioning = true
        statusText.text = "Status: $busyText"
        setButtonsEnabled(false)
        executor.execute {
            val result = try {
                action()
            } catch (e: Exception) {
                RootDaemonManager.Status(
                    running = false,
                    pid = null,
                    ports = emptySet(),
                    message = e.message ?: e.javaClass.simpleName,
                    logTail = "<no log>",
                )
            }
            mainHandler.post {
                isTransitioning = false
                renderStatus(result)
                setButtonsEnabled(true)
            }
        }
    }

    private fun renderStatus(status: RootDaemonManager.Status) {
        val state = if (status.running) "RUNNING" else "STOPPED"
        val pidText = status.pid?.let { " | pid $it" } ?: ""
        statusText.text = "Status: $state$pidText — ${status.message}"
        portsText.text = "Ports: ${status.ports.sorted().joinToString(", ").ifBlank { "-" }}"
        logText.text = status.logTail
        stopButton.isEnabled = status.running
    }

    private fun setButtonsEnabled(enabled: Boolean) {
        startButton.isEnabled = enabled
        restartButton.isEnabled = enabled
        refreshButton.isEnabled = enabled
        stopButton.isEnabled = enabled
        adbEnableButton.isEnabled = enabled
        adbDisableButton.isEnabled = enabled
        adbRefreshButton.isEnabled = enabled
    }

    private fun enableAdb() {
        adbEnableButton.isEnabled = false
        adbDisableButton.isEnabled = false
        adbStatusText.text = "ADB: Enabling..."
        executor.execute {
            val status = RootDaemonManager.enableWirelessAdb()
            mainHandler.post {
                renderAdbStatus(status)
                adbEnableButton.isEnabled = true
                adbDisableButton.isEnabled = true
            }
        }
    }

    private fun disableAdb() {
        adbEnableButton.isEnabled = false
        adbDisableButton.isEnabled = false
        adbStatusText.text = "ADB: Disabling..."
        executor.execute {
            val status = RootDaemonManager.disableWirelessAdb()
            mainHandler.post {
                renderAdbStatus(status)
                adbEnableButton.isEnabled = true
                adbDisableButton.isEnabled = true
            }
        }
    }

    private fun refreshAdbStatus(showBusy: Boolean = true) {
        if (showBusy) adbStatusText.text = "ADB: Checking..."
        executor.execute {
            val status = RootDaemonManager.wirelessAdbStatus()
            mainHandler.post {
                renderAdbStatus(status)
            }
        }
    }

    private fun renderAdbStatus(status: RootDaemonManager.WirelessAdbStatus) {
        val state = when {
            status.listening -> "LISTENING on port ${status.port}"
            status.enabled -> "ENABLED (not listening)"
            else -> "DISABLED"
        }
        adbStatusText.text = "ADB: $state — ${status.message}"
    }

    private fun updateIp() {
        ipText.text = try {
            val wifiManager = applicationContext.getSystemService(WIFI_SERVICE) as WifiManager
            val ip = wifiManager.connectionInfo.ipAddress
            val ipString = String.format(
                "%d.%d.%d.%d",
                ip and 0xff,
                ip shr 8 and 0xff,
                ip shr 16 and 0xff,
                ip shr 24 and 0xff,
            )
            "Phone: $ipString (base port 5000, control API 5008, discovery UDP 5009)"
        } catch (e: Exception) {
            "Phone: Wi‑Fi IP unavailable"
        }
    }
}
