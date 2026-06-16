# AllRelay — Usage Guide

## Build artifacts

```bash
./scripts/build-deb.sh
```

Outputs:
- `bin/allrelay_0.1.0_amd64.deb`
- `bin/scrcpy-server-allrelay`
- `bin/allrelay-magisk.zip`

---

## Android (Phone)

### Option 1: Magisk module

```bash
adb push bin/allrelay-magisk.zip /sdcard/
```

Then open **Magisk → Modules → Install from storage**, choose `allrelay-magisk.zip`, and reboot.

After reboot, the daemon can keep the AllRelay ports available in the background.

### Option 2: Manual ADB run for testing

```bash
adb push bin/scrcpy-server-allrelay /data/local/tmp/allrelay.jar

adb shell "su -c 'CLASSPATH=/data/local/tmp/allrelay.jar app_process / \
  com.genymobile.scrcpy.Server 4.0 \
  log_level=info \
  wifi_mode=true \
  wifi_port=5000 \
  video=true \
  audio=true \
  audio_source=mic \
  speaker_enabled=true \
  camera_enabled=true \
  daemon=true \
  control=true \
  >/data/local/tmp/allrelay-unified.log 2>&1 &'"
```

Useful checks:

```bash
adb shell "su -c 'ss -tlnp | grep -E \"5000|5001|5002|5003|5004|5009\"'"
adb shell "su -c 'head -40 /data/local/tmp/allrelay-unified.log'"
```

---

## Ubuntu (PC)

### Install the package

```bash
sudo dpkg -i bin/allrelay_0.1.0_amd64.deb
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
| `bin/allrelay_0.1.0_amd64.deb` | Ubuntu package |
| `bin/allrelay-magisk.zip` | Magisk module |
| `bin/allrelay-server` | Built Go server binary |
| `bin/scrcpy-server-allrelay` | Android server artifact used by ADB/app/Magisk |
