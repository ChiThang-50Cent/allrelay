package com.genymobile.scrcpy.audio;

import com.genymobile.scrcpy.AndroidVersions;
import com.genymobile.scrcpy.AsyncProcessor;
import com.genymobile.scrcpy.model.StreamId;
import com.genymobile.scrcpy.util.Ln;

import android.annotation.TargetApi;
import android.media.AudioAttributes;
import android.media.AudioFormat;
import android.media.AudioTrack;
import android.media.MediaCodec;
import android.media.MediaFormat;
import android.os.Build;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;

/**
 * Receives Opus-encoded audio from the PC and plays it through the phone speaker.
 *
 * <p>Receives raw Opus packets (with 16-byte AllRelay header).
 * The Go server demuxes Ogg pages and extracts raw Opus packets.
 * OpusHead + OpusTags are combined into CSD-0 for the MediaCodec decoder.
 */
public final class AudioReversePlayback implements AsyncProcessor {

    private static final int SAMPLE_RATE = AudioConfig.SAMPLE_RATE;
    private static final int CHANNELS = AudioConfig.CHANNELS;
    private static final int CHANNEL_CONFIG = AudioFormat.CHANNEL_OUT_STEREO;
    private static final int ENCODING = AudioConfig.ENCODING;

    private static final int HEADER_SIZE = 16;
    private static final long PACKET_FLAG_CONFIG = 1L << 62;

    private final InputStream inputStream;

    private Thread thread;
    private volatile boolean stopped;
    private AudioTrack audioTrack;

    public AudioReversePlayback(InputStream inputStream) {
        this.inputStream = inputStream;
    }

    @Override
    public void start(TerminationListener listener) {
        thread = new Thread(() -> {
            boolean fatalError = false;
            try {
                play();
            } catch (IOException e) {
                Ln.e("Audio reverse playback error", e);
                fatalError = true;
            } finally {
                Ln.d("Audio reverse playback stopped");
                listener.onTerminated(fatalError);
            }
        }, "audio-reverse");
        thread.start();
    }

    @Override
    public void stop() {
        stopped = true;
        if (thread != null) {
            thread.interrupt();
        }
    }

    @Override
    public void join() throws InterruptedException {
        if (thread != null) {
            thread.join();
        }
    }

    @TargetApi(AndroidVersions.API_23_ANDROID_6_0)
    private void play() throws IOException {
        byte[] headerBuf = new byte[HEADER_SIZE];

        MediaCodec decoder = null;
        boolean decoderStarted = false;

        try {
            // Collect OpusHead + OpusTags for CSD-0
            ByteArrayOutputStream csdBuffer = new ByteArrayOutputStream();
            boolean hasCsd = false;

            // Read first two packets: OpusHead and OpusTags
            for (int i = 0; i < 2; i++) {
                int read = readFully(inputStream, headerBuf, 0, HEADER_SIZE);
                if (read < HEADER_SIZE) {
                    Ln.w("Speaker: connection closed before config packet " + i);
                    return;
                }

                int streamId = ByteBuffer.wrap(headerBuf, 0, 4).order(ByteOrder.BIG_ENDIAN).getInt();
                if (streamId != StreamId.SPEAKER.getId()) {
                    Ln.e("Speaker: unexpected stream ID: 0x" + Integer.toHexString(streamId));
                    return;
                }

                long ptsFlags = ByteBuffer.wrap(headerBuf, 4, 8).order(ByteOrder.BIG_ENDIAN).getLong();
                int size = ByteBuffer.wrap(headerBuf, 12, 4).order(ByteOrder.BIG_ENDIAN).getInt();

                if (size > 0) {
                    byte[] data = new byte[size];
                    readFully(inputStream, data, 0, size);
                    csdBuffer.write(data, 0, size);
                    hasCsd = true;
                    Ln.d("Speaker: config packet " + i + " size=" + size);
                }
            }

            Ln.i("Speaker stream connected, csd=" + csdBuffer.size() + " bytes");

            // Create Opus decoder.
            // AOSP SoftOpus.cpp expects 3 initialization buffers:
            //   1. OpusHead (19 bytes) — parsed as OpusHeader
            //   2. Codec delay (8 bytes, int64 nanoseconds) — pre-skip
            //   3. Seek pre-roll (8 bytes, int64 nanoseconds) — typically 80ms
            // These are fed as regular input (NOT CSD-0 / CODEC_CONFIG
            // because SoftOpus ignores CODEC_CONFIG flag in the init phase).
            decoder = MediaCodec.createDecoderByType(AudioCodec.OPUS.getMimeType());
            MediaFormat format = MediaFormat.createAudioFormat(
                    AudioCodec.OPUS.getMimeType(), SAMPLE_RATE, CHANNELS);

            // Feed OpusHead as CSD-0 — this becomes the first init buffer.
            // The framework submits CSD data as input with CODEC_CONFIG flag,
            // but SoftOpus processes it anyway for the first 3 buffers.
            if (hasCsd) {
                byte[] csdBytes = csdBuffer.toByteArray();
                // Only use OpusHead (19 bytes) for CSD-0, not the full combined buffer.
                // OpusTags contains vendor string which is not needed for decoding.
                ByteBuffer csd0 = ByteBuffer.allocateDirect(csdBytes.length);
                csd0.put(csdBytes);
                csd0.flip();
                format.setByteBuffer("csd-0", csd0);
                Ln.d("Speaker: CSD-0 configured, size=" + csdBytes.length);
            }

            decoder.configure(format, null, null, 0);
            decoder.start();
            decoderStarted = true;

            // Feed remaining init buffers (buffer 2 = codec delay, buffer 3 = seek pre-roll).
            // These must be 8-byte int64 values in nanoseconds, little-endian.
            // Pre-skip = 312 samples at 48kHz = 6500000 ns. We feed as little-endian.
            long codecDelayNs = 6500000L; // 312 samples * 1e9 / 48000
            long seekPreRollNs = 80000000L; // 80ms standard
            for (int i = 0; i < 2; i++) {
                int inputBufferId = decoder.dequeueInputBuffer(10000);
                if (inputBufferId >= 0) {
                    ByteBuffer inputBuffer = decoder.getInputBuffer(inputBufferId);
                    if (inputBuffer != null) {
                        inputBuffer.clear();
                        long value = (i == 0) ? codecDelayNs : seekPreRollNs;
                        inputBuffer.order(ByteOrder.LITTLE_ENDIAN);
                        inputBuffer.putLong(value);
                        inputBuffer.flip();
                        decoder.queueInputBuffer(inputBufferId, 0, 8, 0, 0);
                        Ln.d("Speaker: fed init buffer " + (i + 2) + " value=" + value);
                    }
                }
            }

            // Wait for output format change (triggered by buffer 3)
            // and drain the first output buffer (may contain discarded samples)
            MediaCodec.BufferInfo bufferInfo = new MediaCodec.BufferInfo();
            for (int i = 0; i < 50; i++) {
                int outId = decoder.dequeueOutputBuffer(bufferInfo, 100000);
                if (outId == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                    MediaFormat outFormat = decoder.getOutputFormat();
                    Ln.d("Speaker: output format changed: " + outFormat);
                    break;
                } else if (outId >= 0) {
                    // Discard initial output (contains codec delay samples)
                    decoder.releaseOutputBuffer(outId, false);
                    break;
                }
            }

            // Create AudioTrack
            int minBufferSize = AudioTrack.getMinBufferSize(SAMPLE_RATE, CHANNEL_CONFIG, ENCODING);
            audioTrack = createAudioTrack(minBufferSize);
            audioTrack.play();

            ByteBuffer readBuffer = ByteBuffer.allocate(4096);

            long packetCount = 0;
            long inputFed = 0;
            long outputGot = 0;
            long bytesWritten = 0;
            long lastLogTime = System.currentTimeMillis();

            while (!stopped && !Thread.currentThread().isInterrupted()) {
                int headerBytes = readFully(inputStream, headerBuf, 0, HEADER_SIZE);
                if (headerBytes < HEADER_SIZE) {
                    Ln.d("Speaker: connection closed after " + packetCount + " packets");
                    break;
                }

                long ptsFlags = ByteBuffer.wrap(headerBuf, 4, 8).order(ByteOrder.BIG_ENDIAN).getLong();
                boolean config = (ptsFlags & PACKET_FLAG_CONFIG) != 0;
                int payloadSize = ByteBuffer.wrap(headerBuf, 12, 4).order(ByteOrder.BIG_ENDIAN).getInt();

                if (payloadSize <= 0 || payloadSize > 65536) {
                    Ln.w("Speaker: invalid payload size: " + payloadSize);
                    continue;
                }

                if (readBuffer.capacity() < payloadSize) {
                    readBuffer = ByteBuffer.allocate(payloadSize + 1024);
                }
                readBuffer.clear();
                readBuffer.limit(payloadSize);
                int payloadRead = readFully(inputStream, readBuffer.array(), 0, payloadSize);
                if (payloadRead < payloadSize) {
                    Ln.d("Speaker: incomplete payload");
                    break;
                }
                readBuffer.position(payloadRead);
                readBuffer.flip();

                if (config) {
                    // Should not get more config packets after the first two
                    Ln.d("Speaker: unexpected config packet, size=" + payloadSize);
                    continue;
                }

                packetCount++;

                // Feed Opus data to decoder
                int inputBufferId = decoder.dequeueInputBuffer(10000);
                if (inputBufferId < 0) {
                    Ln.w("Speaker: decoder input timeout at packet " + packetCount);
                    continue;
                }

                ByteBuffer inputBuffer = decoder.getInputBuffer(inputBufferId);
                if (inputBuffer != null) {
                    inputBuffer.clear();
                    inputBuffer.put(readBuffer);
                    readBuffer.flip();
                    inputBuffer.flip();
                    long pts = ptsFlags & 0x1FFFFFFFFFFFFFL;
                    decoder.queueInputBuffer(inputBufferId, 0, payloadSize, pts, 0);
                    inputFed++;
                }

                // Drain output
                while (true) {
                    int outputBufferId = decoder.dequeueOutputBuffer(bufferInfo, 0);
                    if (outputBufferId < 0) {
                        break;
                    }
                    if (outputBufferId == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                        MediaFormat newFormat = decoder.getOutputFormat();
                        Ln.d("Speaker: output format changed: " + newFormat);
                        continue;
                    }
                    ByteBuffer outputBuffer = decoder.getOutputBuffer(outputBufferId);
                    if (outputBuffer != null && bufferInfo.size > 0) {
                        byte[] pcmData = new byte[bufferInfo.size];
                        outputBuffer.position(bufferInfo.offset);
                        outputBuffer.get(pcmData, 0, bufferInfo.size);
                        int written = audioTrack.write(pcmData, 0, bufferInfo.size);
                        if (written > 0) {
                            outputGot++;
                            bytesWritten += written;
                        }
                    }
                    decoder.releaseOutputBuffer(outputBufferId, false);
                }

                long now = System.currentTimeMillis();
                if (now - lastLogTime >= 5000) {
                    Ln.d("Speaker: pkts=" + packetCount + " fed=" + inputFed
                            + " out=" + outputGot + " outBytes=" + bytesWritten
                            + " outSize=" + bufferInfo.size);
                    lastLogTime = now;
                }
            }
        } catch (Exception e) {
            Ln.e("Audio reverse playback error: " + e.getMessage(), e);
            throw new IOException(e);
        } finally {
            if (audioTrack != null) {
                try { audioTrack.stop(); } catch (Exception ignored) {}
                try { audioTrack.release(); } catch (Exception ignored) {}
            }
            if (decoder != null) {
                if (decoderStarted) {
                    try { decoder.stop(); } catch (Exception ignored) {}
                }
                try { decoder.release(); } catch (Exception ignored) {}
            }
            if (inputStream != null) {
                try { inputStream.close(); } catch (Exception ignored) {}
            }
        }
    }

    @TargetApi(AndroidVersions.API_23_ANDROID_6_0)
    private AudioTrack createAudioTrack(int minBufferSize) {
        if (Build.VERSION.SDK_INT >= AndroidVersions.API_26_ANDROID_8_0) {
            return new AudioTrack.Builder()
                    .setAudioAttributes(new AudioAttributes.Builder()
                            .setUsage(AudioAttributes.USAGE_MEDIA)
                            .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                            .build())
                    .setAudioFormat(new AudioFormat.Builder()
                            .setEncoding(ENCODING)
                            .setSampleRate(SAMPLE_RATE)
                            .setChannelMask(CHANNEL_CONFIG)
                            .build())
                    .setBufferSizeInBytes(Math.max(minBufferSize, 4 * minBufferSize))
                    .setPerformanceMode(AudioTrack.PERFORMANCE_MODE_LOW_LATENCY)
                    .build();
        }
        @SuppressWarnings("deprecation")
        AudioTrack track = new AudioTrack(
                AudioAttributes.USAGE_MEDIA,
                AudioAttributes.CONTENT_TYPE_MUSIC,
                SAMPLE_RATE,
                CHANNEL_CONFIG,
                ENCODING,
                Math.max(minBufferSize, 4 * minBufferSize),
                AudioTrack.MODE_STREAM);
        return track;
    }

    private static int readFully(InputStream in, byte[] buf, int off, int len) throws IOException {
        int totalRead = 0;
        while (totalRead < len) {
            int r = in.read(buf, off + totalRead, len - totalRead);
            if (r < 0) break;
            totalRead += r;
        }
        return totalRead;
    }
}
