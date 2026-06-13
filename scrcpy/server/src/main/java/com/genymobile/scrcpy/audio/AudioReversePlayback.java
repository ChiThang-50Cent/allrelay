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
 * Uses a static MediaCodec decoder that is created once and flushed/reused
 * across connections — eliminating the ~2s decoder init delay per connection.
 */
public final class AudioReversePlayback implements AsyncProcessor {

    private static final int SAMPLE_RATE = AudioConfig.SAMPLE_RATE;
    private static final int CHANNELS = AudioConfig.CHANNELS;
    private static final int CHANNEL_CONFIG = AudioFormat.CHANNEL_OUT_STEREO;
    private static final int ENCODING = AudioConfig.ENCODING;

    private static final int HEADER_SIZE = 16;
    private static final long PACKET_FLAG_CONFIG = 1L << 62;

    // Shared decoder — created once, flushed on reconnect.
    private static MediaCodec sharedDecoder;
    private static boolean sharedDecoderStarted;
    private static final Object decoderLock = new Object();

    private final InputStream inputStream;

    private Thread thread;
    private volatile boolean stopped;
    private AudioTrack audioTrack;

    public AudioReversePlayback(InputStream inputStream) {
        this.inputStream = inputStream;
    }

    /**
     * Get or create a decoder for this speaker session.
     * Creates a fresh decoder per connection to avoid state corruption
     * when old goroutines hold stale references.
     */
    private static MediaCodec getFreshDecoder() {
        synchronized (decoderLock) {
            // Release old decoder if any
            if (sharedDecoder != null) {
                try {
                    sharedDecoder.stop();
                } catch (Exception ignored) {}
                try {
                    sharedDecoder.release();
                } catch (Exception ignored) {}
                sharedDecoder = null;
            }
            sharedDecoderStarted = false;

            try {
                sharedDecoder = MediaCodec.createDecoderByType(AudioCodec.OPUS.getMimeType());
                Ln.i("Speaker: fresh decoder created");
            } catch (Exception e) {
                Ln.e("Speaker: failed to create decoder", e);
                return null;
            }
            return sharedDecoder;
        }
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

        // Read OpusHead and OpusTags from the stream
        ByteArrayOutputStream csdBuffer = new ByteArrayOutputStream();

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

            int size = ByteBuffer.wrap(headerBuf, 12, 4).order(ByteOrder.BIG_ENDIAN).getInt();
            if (size > 0) {
                byte[] data = new byte[size];
                readFully(inputStream, data, 0, size);
                csdBuffer.write(data, 0, size);
            }
        }

        Ln.i("Speaker stream connected, csd=" + csdBuffer.size() + " bytes");

        // Create a fresh decoder per speaker session (avoids stale state from old goroutines)
        MediaCodec decoder = getFreshDecoder();
        if (decoder == null) {
            Ln.e("Speaker: no decoder available");
            return;
        }

        // Feed CSD-0 for this stream
        byte[] csdBytes = csdBuffer.toByteArray();
        MediaFormat format = MediaFormat.createAudioFormat(
                AudioCodec.OPUS.getMimeType(), SAMPLE_RATE, CHANNELS);
        ByteBuffer csd0 = ByteBuffer.allocateDirect(csdBytes.length);
        csd0.put(csdBytes);
        csd0.flip();
        format.setByteBuffer("csd-0", csd0);

        decoder.configure(format, null, null, 0);
        decoder.start();
        sharedDecoderStarted = true;

        // Feed init buffers (codec delay + seek pre-roll)
        long codecDelayNs = 6500000L;
        long seekPreRollNs = 80000000L;
        for (int i = 0; i < 2; i++) {
            int inputBufferId = decoder.dequeueInputBuffer(50000);
            if (inputBufferId >= 0) {
                ByteBuffer inputBuffer = decoder.getInputBuffer(inputBufferId);
                if (inputBuffer != null) {
                    inputBuffer.clear();
                    long value = (i == 0) ? codecDelayNs : seekPreRollNs;
                    inputBuffer.order(ByteOrder.LITTLE_ENDIAN);
                    inputBuffer.putLong(value);
                    inputBuffer.flip();
                    decoder.queueInputBuffer(inputBufferId, 0, 8, 0, 0);
                }
            }
        }

        // Wait for decoder output — much faster on reconnect (decoder is warm)
        MediaCodec.BufferInfo bufferInfo = new MediaCodec.BufferInfo();
        long initStartMs = System.currentTimeMillis();
        for (int i = 0; i < 10; i++) {
            int outId = decoder.dequeueOutputBuffer(bufferInfo, 50000);
            if (outId == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                Ln.i("Speaker: decoder ready in " + (System.currentTimeMillis() - initStartMs) + "ms (format change)");
                break;
            } else if (outId >= 0) {
                decoder.releaseOutputBuffer(outId, false);
                Ln.i("Speaker: decoder ready in " + (System.currentTimeMillis() - initStartMs) + "ms (first output)");
                break;
            }
        }

        // Create AudioTrack (low latency)
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
                continue;
            }

            packetCount++;

            int inputBufferId = decoder.dequeueInputBuffer(10000);
            if (inputBufferId < 0) continue;

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

            while (true) {
                int outputBufferId = decoder.dequeueOutputBuffer(bufferInfo, 0);
                if (outputBufferId < 0) break;
                if (outputBufferId == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) continue;

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
                        + " out=" + outputGot + " outBytes=" + bytesWritten);
                lastLogTime = now;
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
                    .setBufferSizeInBytes(Math.max(minBufferSize, 2 * minBufferSize))
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
                Math.max(minBufferSize, 2 * minBufferSize),
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
