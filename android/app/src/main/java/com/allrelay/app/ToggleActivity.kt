package com.allrelay.app

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.widget.Button
import android.widget.LinearLayout
import android.widget.Switch
import android.widget.TextView
import android.view.Gravity
import android.view.View
import android.graphics.Color
import android.graphics.Typeface
import android.util.TypedValue

/**
 * ToggleActivity - Main UI for AllRelay
 *
 * Provides toggle switches for each stream:
 * - Screen (Monitor)
 * - Camera
 * - Microphone
 * - Speaker
 *
 * Each toggle can be enabled/disabled independently.
 * Shows connection status and device info.
 */
class ToggleActivity : Activity() {

    private lateinit var statusText: TextView
    private lateinit var ipText: TextView
    private lateinit var screenSwitch: Switch
    private lateinit var cameraSwitch: Switch
    private lateinit var micSwitch: Switch
    private lateinit var speakerSwitch: Switch
    private lateinit var startButton: Button
    private lateinit var stopButton: Button

    private var isRunning = false

    companion object {
        const val ACTION_TOGGLE_STREAM = "com.allrelay.TOGGLE_STREAM"
        const val EXTRA_STREAM_NAME = "stream_name"
        const val EXTRA_ENABLED = "enabled"
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val layout = createLayout()
        setContentView(layout)

        // Start AllRelay service
        startAllRelayService()
    }

    private fun createLayout(): LinearLayout {
        val layout = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(48, 48, 48, 48)
            setBackgroundColor(Color.parseColor("#1a1a2e"))
        }

        // Title
        val title = TextView(this).apply {
            text = "AllRelay"
            setTextColor(Color.parseColor("#e94560"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 32f)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setPadding(0, 0, 0, 32)
        }
        layout.addView(title)

        // Status
        statusText = TextView(this).apply {
            text = "Status: Ready"
            setTextColor(Color.parseColor("#16213e"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
            setPadding(0, 0, 0, 16)
        }
        layout.addView(statusText)

        // IP Address
        ipText = TextView(this).apply {
            text = "IP: Detecting..."
            setTextColor(Color.parseColor("#0f3460"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            setPadding(0, 0, 0, 32)
        }
        layout.addView(ipText)

        // Stream toggles
        val streamHeader = TextView(this).apply {
            text = "Streams"
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 20f)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(0, 0, 0, 16)
        }
        layout.addView(streamHeader)

        screenSwitch = createStreamToggle("Screen", "Mirror phone display", true)
        cameraSwitch = createStreamToggle("Camera", "Use phone as webcam", false)
        micSwitch = createStreamToggle("Microphone", "Use phone mic on PC", false)
        speakerSwitch = createStreamToggle("Speaker", "Play PC audio on phone", false)

        layout.addView(screenSwitch)
        layout.addView(cameraSwitch)
        layout.addView(micSwitch)
        layout.addView(speakerSwitch)

        // Buttons
        val buttonLayout = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
            setPadding(0, 48, 0, 0)
        }

        startButton = Button(this).apply {
            text = "Start AllRelay"
            setOnClickListener { startAllRelay() }
            setBackgroundColor(Color.parseColor("#e94560"))
            setTextColor(Color.WHITE)
            setPadding(32, 16, 32, 16)
        }
        buttonLayout.addView(startButton)

        stopButton = Button(this).apply {
            text = "Stop"
            setOnClickListener { stopAllRelay() }
            setBackgroundColor(Color.parseColor("#533483"))
            setTextColor(Color.WHITE)
            isEnabled = false
            setPadding(32, 16, 32, 16)
        }
        buttonLayout.addView(stopButton)

        layout.addView(buttonLayout)

        return layout
    }

    private fun createStreamToggle(name: String, description: String, defaultOn: Boolean): Switch {
        return Switch(this).apply {
            text = "$name - $description"
            isChecked = defaultOn
            setTextColor(Color.parseColor("#e0e0e0"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
            setPadding(16, 24, 16, 24)
            setBackgroundColor(Color.parseColor("#16213e"))

            val params = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            )
            params.setMargins(0, 8, 0, 8)
            layoutParams = params

            setOnCheckedChangeListener { _, isChecked ->
                onStreamToggleChanged(name.lowercase(), isChecked)
            }
        }
    }

    private fun onStreamToggleChanged(streamName: String, enabled: Boolean) {
        // Send toggle command to AllRelay service
        val intent = Intent(ACTION_TOGGLE_STREAM).apply {
            putExtra(EXTRA_STREAM_NAME, streamName)
            putExtra(EXTRA_ENABLED, enabled)
        }
        sendBroadcast(intent)

        updateStatus("Stream $streamName: ${if (enabled) "ON" else "OFF"}")
    }

    private fun startAllRelay() {
        isRunning = true
        startButton.isEnabled = false
        stopButton.isEnabled = true

        updateStatus("AllRelay started")

        // Collect enabled streams
        val streams = mutableListOf<String>()
        if (screenSwitch.isChecked) streams.add("screen")
        if (cameraSwitch.isChecked) streams.add("camera")
        if (micSwitch.isChecked) streams.add("mic")
        if (speakerSwitch.isChecked) streams.add("speaker")

        updateStatus("Active streams: ${streams.joinToString(", ")}")
    }

    private fun stopAllRelay() {
        isRunning = false
        startButton.isEnabled = true
        stopButton.isEnabled = false

        updateStatus("AllRelay stopped")
    }

    private fun startAllRelayService() {
        // Start the AllRelay background service
        val serviceIntent = Intent(this, AllRelayService::class.java)
        startService(serviceIntent)

        updateIP()
    }

    private fun updateIP() {
        // Get device IP address
        try {
            val wifiManager = applicationContext.getSystemService(WIFI_SERVICE) as android.net.wifi.WifiManager
            val wifiInfo = wifiManager.connectionInfo
            val ip = wifiInfo.ipAddress
            val ipString = String.format(
                "%d.%d.%d.%d",
                ip and 0xff,
                ip shr 8 and 0xff,
                ip shr 16 and 0xff,
                ip shr 24 and 0xff
            )
            ipText.text = "IP: $ipString:5000"
        } catch (e: Exception) {
            ipText.text = "IP: Unable to detect"
        }
    }

    private fun updateStatus(message: String) {
        statusText.text = "Status: $message"
    }
}
