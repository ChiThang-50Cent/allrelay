# AllRelay — Usage Guide

## Android (Phone)

### Cách 1: Magisk Module (tự động sau reboot)

```bash
# Copy zip vào phone
adb push bin/allrelay-magisk.zip /sdcard/

# Mở Magisk → Modules → Install from storage → chọn allrelay-magisk.zip → Reboot
```

Sau reboot, server tự chạy, port 5001 (camera) + 5003 (speaker) luôn mở.
Không cần ADB gì nữa.

### Cách 2: Chạy thủ công qua ADB (để test)

```bash
# Push JAR
adb push bin/scrcpy-server-allrelay /data/local/tmp/allrelay.jar

# Chạy
adb shell "su -c 'CLASSPATH=/data/local/tmp/allrelay.jar app_process / \
  com.genymobile.scrcpy.Server 4.0 \
  log_level=info \
  no_video=true \
  audio=false \
  wifi_mode=true \
  wifi_port=5000 \
  speaker_enabled=true \
  camera_enabled=true \
  daemon=true &'"
```

---

## Ubuntu (PC)

### Cài đặt (1 lần duy nhất, cần sudo)

```bash
# Cài .deb
sudo dpkg -i bin/allrelay_0.1.0_amd64.deb

# Tự động start
sudo systemctl enable --now allrelay
```

### Kiểm tra

```bash
systemctl status allrelay      # xem trạng thái
journalctl -u allrelay -f      # xem log real-time
```

### Sử dụng

1. Mở trình duyệt: **http://localhost:9090**
2. Bấm **"Scan Network"** để tìm phone tự động (2 giây)
3. Hoặc nhập IP phone thủ công (vd: 192.168.1.83)
4. Bấm **"Connect"**
5. Bật/tắt từng stream riêng biệt:
   - 🔊 Speaker: âm thanh PC → phone
   - 📷 Camera: camera phone → PC (xuất hiện trong Meet/Zoom)

### Camera cho Google Meet / Zoom

1. **Bật camera stream trong AllRelay TRƯỚC**
2. **Sau đó mới mở Meet/Zoom** — WebRTC chỉ scan camera lúc load trang
3. Chọn camera **"AllRelay Cam"** trong dropdown

### Gỡ cài đặt

```bash
sudo systemctl stop allrelay
sudo dpkg -r allrelay
```

---

## File cần thiết

| File | Mục đích |
|------|----------|
| `bin/allrelay_0.1.0_amd64.deb` | Package cho Ubuntu |
| `bin/allrelay-magisk.zip` | Magisk module cho Android |
| `bin/allrelay-server` | Go binary (có thể chạy trực tiếp) |
| `bin/scrcpy-server-allrelay` | JAR cho Android (dùng với ADB) |
