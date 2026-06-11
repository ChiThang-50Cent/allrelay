# Root Cause Analysis — AllRelay Debugging Session (2026-06-11)

> Ghi lại nguyên nhân gốc và bài học từ session debug mic/camera/speaker kéo dài cả ngày.

---

## 1. GStreamer fdsrc via Go pipe → "EOS before finding a chain"

**Symptom**: `gst-launch-1.0 fdsrc fd=0 ! oggdemux ! opusdec ...` khởi động nhưng thoát ngay với lỗi "EOS before finding a chain" khi nhận Ogg data từ Go.

**Root cause**: Go's `exec.Cmd.StdinPipe()` tạo pipe, GStreamer `fdsrc` đọc từ pipe. Phone gửi OpusHead config ngay lập tức nhưng audio data bắt đầu sau 5-10 giây. `oggdemux` đọc config, chạm EOF (Go chưa viết audio), và exit.

**Solution**: Dùng FIFO (named pipe) + `filesrc` thay vì `fdsrc fd=0`:

```
1. mkfifo /tmp/allrelay-mic-<pid>.fifo
2. Start gst-launch FIRST (nó mở FIFO để đọc)
3. Mở FIFO với os.O_RDWR (không bao giờ block)
4. Buffer config + 25 audio packets trong Go
5. Ghi tất cả data buffered khi sẵn sàng
```

**Key insight**: `os.O_RDWR` trên FIFO không bao giờ block, khác với `os.O_WRONLY` (block cho đến khi reader mở đầu kia). Loại bỏ race condition nơi gst-launch mở và đóng FIFO trước khi Go ghi byte đầu tiên.

---

## 2. Ogg CRC32 polynomial mismatch

**Symptom**: `oggdemux` reject Ogg pages với "invalid CRC" dù data đúng.

**Root cause**: Ogg dùng **non-reflected** CRC-32 với polynomial `0x04C11DB7`, init=0, không final XOR. zlib/PKZIP CRC-32 dùng **reflected** polynomial `0xEDB88320` với init=0xFFFFFFFF và final XOR. Hai checksum hoàn toàn khác nhau.

**Solution**: Implement Ogg-specific CRC-32:

```go
func oggCRC32(page []byte) uint32 {
    var crc uint32
    for _, b := range page {
        crc = (crc << 8) ^ crc32Table[byte(crc>>24)^b]
    }
    return crc
}
```

Lookup table dùng polynomial `0x04C11DB7`, init=0, không final XOR.

---

## 3. Edge/Chrome audio device discovery trên Linux

**Symptom**: PipeWire `Audio/Source/Virtual` nodes tồn tại nhưng Edge báo "Mic not found".

**Root cause**: Edge/Chrome trên Linux discover audio devices qua **PulseAudio API** (qua `pipewire-pulse` compatibility layer), KHÔNG phải PipeWire native API. PipeWire nodes tạo bằng `pw-cli create-node adapter` hoặc `pw-loopback` với `media.class=Audio/Source/Virtual` **KHÔNG được export** qua pipewire-pulse cho PulseAudio clients.

**Solution**: Dùng PulseAudio modules qua `pactl` để tạo devices mà pipewire-pulse export:

```bash
# Tạo null-sink (tạo cả sink và monitor source)
pactl load-module module-null-sink sink_name=allrelay-mic-sink

# Tạo remap-source (wrap monitor thành Audio/Source chuẩn)
pactl load-module module-remap-source \
    master=allrelay-mic-sink.monitor \
    source_name=AllRelay_Phone_Mic \
    source_properties=device.description="AllRelay Phone Mic"

# Set defaults
pactl set-default-source AllRelay_Phone_Mic
pactl set-default-sink allrelay-speaker-sink
```

**Key insight**: `module-remap-source` tạo source với `media.class=Audio/Source` (không phải Virtual) mà pipewire-pulse export cho PulseAudio clients. Edge/Chrome discover qua `navigator.mediaDevices.enumerateDevices()`.

**Caveat**: Edge cache device list khi khởi động. PHẢI restart Edge hoàn toàn (đóng tất cả cửa sổ, kill process) để detect devices mới.

---

## 4. v4l2loopback `exclusive_caps` cho Edge camera detection

**Symptom**: ffmpeg fail với `ioctl(VIDIOC_G_FMT): Invalid argument` khi ghi vào `/dev/video10`.

**Root cause**: v4l2loopback không có `exclusive_caps=1` expose cả capture và output capabilities. Khi reader (như Edge) mở device, nó có thể set capture format, conflict với ffmpeg output format. Với `exclusive_caps=1`, device chỉ expose output capability, ffmpeg set format không conflict.

**Solution**:
```bash
sudo modprobe v4l2loopback video_nr=10 card_label="AllRelay Cam" exclusive_caps=1
```

**Caveat**: `card_label` KHÔNG được chứa "loopback" — Chrome/Edge filter bỏ V4L2 devices có "loopback" trong tên.

---

## 5. PipeWire IEC958 graph suspend (Speaker blocker)

**Symptom**: `pulsesrc`, `pipewiresrc`, `pw-record`, `pw-cat` đều fail khi capture audio từ speaker output.

**Root cause**: Hệ thống dùng IEC958 (S/PDIF) digital output không có receiver vật lý. ALSA/PipeWire không activate audio graph — tất cả nodes hiển thị `QUANT=0, RATE=0`. Không có graph active, không thể capture audio.

**Mitigation**: Tạo PulseAudio null-sink cho Edge speaker output. Go server có thể capture từ null-sink monitor khi graph active. Fix lâu dài: native PipeWire capture client (cgo/libpipewire) hoặc virtual null-sink driver.

---

## 6. opusenc VBR frames crash Android MediaCodec

**Symptom**: Android `AudioReversePlayback` crash với `dequeueOutputBuffer` error khi nhận Opus packets.

**Root cause**: Default VBR opusenc tạo variable-size frames, bao gồm tiny 3-byte "silence" frames. Android MediaCodec Opus decoder không xử lý được tiny frames.

**Solution**: Dùng CBR mode với fixed frame size:
```bash
opusenc bitrate=64000 bitrate-type=cbr frame-size=20
```

---

## 7. Parallel TCP connection deadlock

**Symptom**: Go server kết nối 5 ports tuần tự, nhưng phone `ServerSocket.accept()` có deadline 12 giây. Kết nối tuần tự mất >12s, causing ports sau timeout.

**Solution**: Kết nối tất cả ports song song bằng goroutines:

```go
var wg sync.WaitGroup
for _, port := range ports {
    wg.Add(1)
    go func(p int) {
        defer wg.Done()
        conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
        // ...
    }(port)
}
wg.Wait()
```

---

## Debugging Checklist

Khi audio/video không hoạt động trong Edge/Chrome:

1. **Kiểm tra PulseAudio nodes**: `pactl list short sources` / `pactl list short sinks`
2. **Kiểm tra default devices**: `pactl get-default-source` / `pactl get-default-sink`
3. **Kiểm tra V4L2 device**: `v4l2-ctl -d /dev/video10 --all` (xem `exclusive_caps`)
4. **Kiểm tra ffmpeg/gst-launch**: `ps aux | grep -E "ffmpeg|gst-launch"`
5. **Kiểm tra server logs**: `cat /tmp/allrelay-*.log`
6. **Restart Edge hoàn toàn**: `pkill -f microsoft-edge` rồi mở lại
7. **Kiểm tra phone logs**: `adb shell "su -c 'cat /data/allrelay/logs/allrelay.log'"`
