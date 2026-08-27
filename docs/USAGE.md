# AllRelay — Usage Guide

## Build artifacts

### Ubuntu package

```bash
./scripts/build-deb.sh
```

Output:
- `bin/allrelay_0.2.0_amd64.deb`

### Android APK

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
sudo dpkg -i bin/allrelay_0.2.0_amd64.deb
systemctl --user enable --now allrelay
```

### Check service status

```bash
systemctl --user status allrelay
journalctl --user -u allrelay -f
```

### Open the dashboard

```text
http://localhost:9090
```

### Typical flow

1. Click **Scan** to find the phone via UDP subnet scan
2. Or enter the phone IP manually
3. Click **Connect**
4. Toggle streams independently:
   - **Screen** → opens the remote popup
   - **Camera** → exposes the phone camera on Linux via `v4l2loopback`
   - **Mic** → exposes the phone mic as a Linux audio input
   - **Speaker** → plays PC audio on the phone

### Camera tips for Meet / Zoom / Chrome

1. Start the **Camera** stream first
2. Then open the meeting app/page
3. Select the AllRelay virtual camera

---

## Files

| File | Purpose |
|------|---------|
| `bin/allrelay_0.2.0_amd64.deb` | Ubuntu package |
| `bin/allrelay-app-debug.apk` | Android APK with bundled server |
