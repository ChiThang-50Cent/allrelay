# AllRelay — Usage Guide

## Build artifacts

### Ubuntu package

```bash
./scripts/build-deb.sh
```

Output:
- `bin/allrelay_0.2.18_amd64.deb`

### Android APK

Use a full JDK 21 and set `JAVA_HOME` and `ANDROID_HOME` to the installed JDK and Android SDK. The backend source build requires Go 1.24+, `pkg-config`, and `libopus-dev`.

```bash
./scripts/build-apk.sh
```

Output:
- `bin/allrelay-app-debug.apk`

---

## Android (Phone)

Install `bin/allrelay-app-debug.apk`, open it, and tap **Start**. The app launches the daemon with the selected stream toggles.
If an older AllRelay Magisk module is installed, remove it in **Magisk → Modules** and reboot before using the APK. This removes the former boot-time daemon; after reboot, open the APK and tap **Start**.

- **Start** — launches the daemon if not running
- **Stop** — kills the daemon
- **Restart** — stop then start

Root access is required only while the app starts or stops the `app_process` daemon, or when it changes wireless ADB settings. After reboot, open the app and tap **Start** again; the daemon does not auto-start.

---

## Ubuntu (PC)

### Install the package

```bash
sudo dpkg -i bin/allrelay_0.2.18_amd64.deb
allrelay tray
```

### Check service status

```bash
systemctl --user status allrelay
journalctl --user -u allrelay -f
```

### Open the dashboard

```bash
allrelay open
allrelay url
```

The dashboard binds to `127.0.0.1` on an automatically selected port. Use the URL printed by `allrelay url`; neither user service auto-starts at login.

### Typical flow

1. Click **Scan** to find the phone via UDP subnet scan
2. Or enter the phone IP manually
3. Click **Connect**
4. Toggle streams independently:
   - **Screen** → opens the remote popup
   - **Camera** → exposes the phone camera on Linux via `v4l2loopback`
   - **Mic** → exposes the phone mic as a Linux audio input
   - **Speaker** → plays PC audio on the phone

Stream controls are unavailable until a phone connection succeeds. Failed starts leave the stream inactive and report the API error. The connected phone's name/IP remains visible after refreshing the dashboard.

Latency values are estimates, not measured end-to-end capture/playback latency.

Screen ON keeps the popup open while the stream starts. Refreshing or navigating the popup does not stop mirroring; closing it stops only Screen while other streams continue.

Screen checks the phone's physical fold state at the start of each session. On a folded phone, the Android daemon requests a firmware-supported fully opened state so it can capture the main display; on an unfolded or non-foldable phone, it does not request a fold override. During an active Screen session, Android stays awake for remote interaction while the physical display panels stay off.

On the tested Galaxy Z Flip3, waking a sleeping phone briefly unblanked the panel driver before Screen reasserted OFF. Startup sampling caught one non-OFF sample at roughly 100 ms intervals, with both brightness readings remaining zero. Screen does not guarantee zero transient unblank during wake; a remote recording cannot establish whether the physical panel emitted light.

Stopping Screen releases only the fold override owned by that video session and returns display handling to the phone's normal fold and sleep policies. This does not force the inner panel on while the phone is folded: the cover display and ordinary power-button behavior remain under Android's control. On an open phone, normal display restoration and sleep timeout apply. An existing opened-state override can be used without taking ownership, while a conflicting external override prevents startup. For setup failures, inspect the Android app's daemon log for the detailed fold/display error.

Fold-state preparation runs on the phone; normal Screen use does not require ADB or a desktop scrcpy installation.

### Camera tips for Meet / Zoom / Chrome

1. Start the **Camera** stream first
2. Then open the meeting app/page
3. Select the AllRelay virtual camera

The Android camera profile is 640×480 at 15 FPS with a 3 Mbps encoder target, including when screen mirroring is enabled.

---

## Files

| File | Purpose |
|------|---------|
| `bin/allrelay_0.2.18_amd64.deb` | Ubuntu package |
| `bin/allrelay-app-debug.apk` | Android APK with bundled server |
