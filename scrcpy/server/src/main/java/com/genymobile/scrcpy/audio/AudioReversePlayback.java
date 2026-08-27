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
import android.os.Process;

import java.io.IOException;
import java.io.InputStream;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.util.concurrent.atomic.AtomicBoolean;

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
    private static final long MAX_PLAYBACK_LAG_US = 100_000L;
    private static final byte[] OPUS_HEAD_MAGIC = {'O', 'p', 'u', 's', 'H', 'e', 'a', 'd'};


    // Shared decoder — created once, flushed on reconnect.
    private static MediaCodec sharedDecoder;
    private static boolean sharedDecoderStarted;
    private static final Object decoderLock = new Object();
    private static final AtomicBoolean playbackActive = new AtomicBoolean(false);

    private final InputStream inputStream;

    private Thread thread;
    private volatile boolean stopped;
    private AudioTrack audioTrack;

    public AudioReversePlayback(InputStream inputStream) {
        this.inputStream = inputStream;
    }

    public static boolean isPlaybackActive() {
        return playbackActive.get();
    }

    // Keep transport/codec backlogs from becoming audible latency.
    static boolean isStalePlaybackPacket(long packetPtsUs, long firstPacketPtsUs, long elapsedUs) {

        return elapsedUs - (packetPtsUs - firstPacketPtsUs) > MAX_PLAYBACK_LAG_US;
    }

    static boolean isValidOpusHead(byte[] data) {
        if (data == null || data.length < 19 || data[8] != 1 || data[9] == 0) {
            return false;
        }
        for (int i = 0; i < OPUS_HEAD_MAGIC.length; ++i) {
            if (data[i] != OPUS_HEAD_MAGIC[i]) {
                return false;
            }
        }
        return true;
    }

    static ByteBuffer createNativeOrderLongCsd(long value) {
        ByteBuffer csd = ByteBuffer.allocate(8).order(ByteOrder.nativeOrder());
        csd.putLong(value);
        csd.flip();
        return csd;
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
                Process.setThreadPriority(Process.THREAD_PRIORITY_URGENT_AUDIO);
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
        playbackActive.set(true);
        try {
            byte[] headerBuf = new byte[HEADER_SIZE];

            // Read OpusHead and OpusTags from the stream.
            byte[] opusHead = null;
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
                if (size <= 0 || size > 65536) {
                    Ln.e("Speaker: invalid config packet size: " + size);
                    return;
                }
                byte[] data = new byte[size];
                if (readFully(inputStream, data, 0, size) < size) {
                    Ln.w("Speaker: connection closed in config packet " + i);
                    return;
                }
                if (i == 0) {
                    if (!isValidOpusHead(data)) {
                        Ln.e("Speaker: invalid OpusHead");
                        return;
                    }
                    opusHead = data;
                }
            }

            Ln.i("Speaker stream connected, OpusHead=" + opusHead.length + " bytes");

            // Create a fresh decoder per speaker session (avoids stale state from old goroutines)
            MediaCodec decoder = getFreshDecoder();
            if (decoder == null) {
                Ln.e("Speaker: no decoder available");
                return;
            }

            // Android's Opus decoder consumes all codec-specific data at start.
            MediaFormat format = MediaFormat.createAudioFormat(
                    AudioCodec.OPUS.getMimeType(), SAMPLE_RATE, CHANNELS);
            format.setByteBuffer("csd-0", ByteBuffer.wrap(opusHead));
            format.setByteBuffer("csd-1", createNativeOrderLongCsd(6_500_000L));
            format.setByteBuffer("csd-2", createNativeOrderLongCsd(80_000_000L));

            decoder.configure(format, null, null, 0);
            decoder.start();
            sharedDecoderStarted = true;

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
            long firstPacketPtsUs = -1;
            long playbackStartedNs = 0;
            long stalePackets = 0;

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
                long pts = ptsFlags & 0x1FFFFFFFFFFFFFL;
                if (firstPacketPtsUs < 0) {
                    firstPacketPtsUs = pts;
                    playbackStartedNs = System.nanoTime();
                }
                long elapsedUs = (System.nanoTime() - playbackStartedNs) / 1_000L;
                if (isStalePlaybackPacket(pts, firstPacketPtsUs, elapsedUs)) {
                    stalePackets++;
                    continue;
                }


                int inputBufferId = decoder.dequeueInputBuffer(10000);
                if (inputBufferId < 0) continue;

                ByteBuffer inputBuffer = decoder.getInputBuffer(inputBufferId);
                if (inputBuffer != null) {
                    inputBuffer.clear();
                    inputBuffer.put(readBuffer);
                    readBuffer.flip();
                    inputBuffer.flip();
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
                    Ln.d("Speaker: pkts=" + packetCount + " stale=" + stalePackets + " fed=" + inputFed
                            + " out=" + outputGot + " outBytes=" + bytesWritten);
                    lastLogTime = now;
                }
            }
        } finally {
            playbackActive.set(false);
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
                    .setBufferSizeInBytes(minBufferSize)
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
                minBufferSize,
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
