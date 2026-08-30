package com.genymobile.scrcpy.audio;

import com.genymobile.scrcpy.AndroidVersions;
import com.genymobile.scrcpy.AsyncProcessor;
import com.genymobile.scrcpy.model.StreamId;
import com.genymobile.scrcpy.util.Ln;

import android.annotation.TargetApi;
import android.media.AudioAttributes;
import android.media.AudioDeviceInfo;
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
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Receives Opus-encoded audio from the PC and plays it through the phone speaker.
 *
 * <p>A receive thread keeps the transport current while a playback thread owns
 * the decoder and AudioTrack.
 */
public final class AudioReversePlayback implements AsyncProcessor {

    private static final int SAMPLE_RATE = AudioConfig.SAMPLE_RATE;
    private static final int CHANNELS = AudioConfig.CHANNELS;
    private static final int CHANNEL_CONFIG = AudioFormat.CHANNEL_OUT_STEREO;
    private static final int ENCODING = AudioConfig.ENCODING;

    private static final int HEADER_SIZE = 16;
    private static final long PACKET_FLAG_CONFIG = 1L << 62;
    private static final byte[] OPUS_HEAD_MAGIC = {'O', 'p', 'u', 's', 'H', 'e', 'a', 'd'};
    private static final int PACKET_QUEUE_CAPACITY = 25;
    private static final long EOS_DEQUEUE_TIMEOUT_US = 10_000;
    private static final int EOS_MAX_DEQUEUE_RETRIES = 100;

    private static final AtomicBoolean playbackActive = new AtomicBoolean(false);

    private final InputStream inputStream;
    private final ArrayBlockingQueue<Packet> packetQueue = new ArrayBlockingQueue<>(PACKET_QUEUE_CAPACITY);
    private final AtomicLong droppedPacketCount = new AtomicLong();
    private final AtomicLong receivedPacketCount = new AtomicLong();
    private final AtomicBoolean receiverFinished = new AtomicBoolean();
    private final AtomicBoolean fatalError = new AtomicBoolean();

    private Thread thread;
    private Thread playbackThread;
    private volatile boolean stopped;

    public AudioReversePlayback(InputStream inputStream) {
        this.inputStream = inputStream;
    }

    public static boolean isPlaybackActive() {
        return playbackActive.get();
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

    private static MediaCodec createDecoder() {
        try {
            MediaCodec decoder = MediaCodec.createDecoderByType(AudioCodec.OPUS.getMimeType());
            Ln.i("Speaker: decoder created");
            return decoder;
        } catch (Exception e) {
            Ln.e("Speaker: failed to create decoder", e);
            return null;
        }
    }

    @Override
    public void start(TerminationListener listener) {
        thread = new Thread(() -> {
            try {
                byte[] opusHead = readConfiguration();
                if (opusHead != null && !stopped) {
                    startPlayback(opusHead);
                    receivePackets();
                }
            } catch (IOException e) {
                if (!stopped) {
                    Ln.e("Audio reverse playback error", e);
                    fatalError.set(true);
                    stopped = true;
                }
            } finally {
                receiverFinished.set(true);
                wakePlayback();
                joinPlayback();
                Ln.d("Audio reverse playback stopped");
                listener.onTerminated(fatalError.get());
            }
        }, "audio-reverse");
        thread.start();
    }

    @Override
    public void stop() {
        stopped = true;
        closeInputStream();
        Thread receiver = thread;
        if (receiver != null) {
            receiver.interrupt();
        }
        wakePlayback();
    }

    @Override
    public void join() throws InterruptedException {
        if (thread != null) {
            thread.join();
        }
    }

    private byte[] readConfiguration() throws IOException {
        byte[] headerBuf = new byte[HEADER_SIZE];
        byte[] opusHead = null;
        for (int i = 0; i < 2; i++) {
            int read = readFully(inputStream, headerBuf, 0, HEADER_SIZE);
            if (read < HEADER_SIZE) {
                if (!stopped) {
                    Ln.w("Speaker: connection closed before config packet " + i);
                }
                return null;
            }

            int streamId = ByteBuffer.wrap(headerBuf, 0, 4).order(ByteOrder.BIG_ENDIAN).getInt();
            if (streamId != StreamId.SPEAKER.getId()) {
                Ln.e("Speaker: unexpected stream ID: 0x" + Integer.toHexString(streamId));
                return null;
            }

            int size = ByteBuffer.wrap(headerBuf, 12, 4).order(ByteOrder.BIG_ENDIAN).getInt();
            if (size <= 0 || size > 65536) {
                Ln.e("Speaker: invalid config packet size: " + size);
                return null;
            }
            byte[] data = new byte[size];
            if (readFully(inputStream, data, 0, size) < size) {
                if (!stopped) {
                    Ln.w("Speaker: connection closed in config packet " + i);
                }
                return null;
            }
            if (i == 0) {
                if (!isValidOpusHead(data)) {
                    Ln.e("Speaker: invalid OpusHead");
                    return null;
                }
                opusHead = data;
            }
        }

        Ln.i("Speaker stream connected, OpusHead=" + opusHead.length + " bytes");
        return opusHead;
    }

    private void startPlayback(byte[] opusHead) {
        playbackThread = new Thread(() -> {
            playbackActive.set(true);
            try {
                Process.setThreadPriority(Process.THREAD_PRIORITY_URGENT_AUDIO);
                playPackets(opusHead);
            } catch (Exception e) {
                if (!stopped) {
                    Ln.e("Audio reverse playback error", e);
                    fatalError.set(true);
                    stopReceiving();
                }
            } finally {
                playbackActive.set(false);
                if (!receiverFinished.get() && !stopped) {
                    stopReceiving();
                }
            }
        }, "audio-reverse-playback");
        playbackThread.start();
    }

    private void receivePackets() throws IOException {
        byte[] headerBuf = new byte[HEADER_SIZE];
        while (!stopped && !Thread.currentThread().isInterrupted()) {
            int headerBytes = readFully(inputStream, headerBuf, 0, HEADER_SIZE);
            if (headerBytes < HEADER_SIZE) {
                Ln.d("Speaker: connection closed after " + receivedPacketCount.get() + " packets");
                return;
            }

            long ptsFlags = ByteBuffer.wrap(headerBuf, 4, 8).order(ByteOrder.BIG_ENDIAN).getLong();
            boolean config = (ptsFlags & PACKET_FLAG_CONFIG) != 0;
            int payloadSize = ByteBuffer.wrap(headerBuf, 12, 4).order(ByteOrder.BIG_ENDIAN).getInt();
            if (payloadSize <= 0 || payloadSize > 65536) {
                continue;
            }

            byte[] payload = new byte[payloadSize];
            if (readFully(inputStream, payload, 0, payloadSize) < payloadSize) {
                Ln.d("Speaker: incomplete payload");
                return;
            }
            if (config) {
                continue;
            }

            receivedPacketCount.incrementAndGet();
            enqueuePacket(new Packet(payload, ptsFlags & 0x1FFFFFFFFFFFFFL));
        }
    }

    private void enqueuePacket(Packet packet) {
        if (packetQueue.offer(packet)) {
            return;
        }
        Packet dropped = packetQueue.poll();
        if (dropped != null) {
            droppedPacketCount.incrementAndGet();
        }
        if (!packetQueue.offer(packet)) {
            droppedPacketCount.incrementAndGet();
        }
    }

    @TargetApi(AndroidVersions.API_23_ANDROID_6_0)
    private void playPackets(byte[] opusHead) throws IOException {
        MediaCodec decoder = null;
        AudioTrack audioTrack = null;
        boolean decoderStarted = false;
        try {
            decoder = createDecoder();
            if (decoder == null) {
                Ln.e("Speaker: no decoder available");
                return;
            }

            MediaFormat format = MediaFormat.createAudioFormat(
                    AudioCodec.OPUS.getMimeType(), SAMPLE_RATE, CHANNELS);
            format.setByteBuffer("csd-0", ByteBuffer.wrap(opusHead));
            format.setByteBuffer("csd-1", createNativeOrderLongCsd(6_500_000L));
            format.setByteBuffer("csd-2", createNativeOrderLongCsd(80_000_000L));

            decoder.configure(format, null, null, 0);
            decoder.start();
            decoderStarted = true;

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

            int minBufferSize = AudioTrack.getMinBufferSize(SAMPLE_RATE, CHANNEL_CONFIG, ENCODING);
            int pcmFrameSize = CHANNELS * AudioConfig.BYTES_PER_SAMPLE;
            int bufferSize = ((minBufferSize * 2 + pcmFrameSize - 1) / pcmFrameSize) * pcmFrameSize;
            int playbackStartThreshold = bufferSize / 2;
            audioTrack = createAudioTrack(bufferSize);
            if (audioTrack.getState() != AudioTrack.STATE_INITIALIZED) {
                Ln.e("Speaker: AudioTrack initialization failed, state=" + audioTrack.getState());
                return;
            }

            long inputFed = 0;
            PcmWriter pcmWriter = new PcmWriter(audioTrack, playbackStartThreshold);
            Packet pendingPacket = null;
            long lastLogTime = 0;

            while (!stopped) {
                long now = System.currentTimeMillis();
                if (now - lastLogTime >= 5000) {
                    int bufferSizeFrames = Build.VERSION.SDK_INT >= AndroidVersions.API_23_ANDROID_6_0
                            ? getBufferSizeInFrames(audioTrack)
                            : -1;
                    int bufferCapacityFrames = Build.VERSION.SDK_INT >= AndroidVersions.API_24_ANDROID_7_0
                            ? getBufferCapacityInFrames(audioTrack)
                            : -1;
                    int underruns = Build.VERSION.SDK_INT >= AndroidVersions.API_24_ANDROID_7_0
                            ? getUnderrunCount(audioTrack)
                            : -1;
                    String route = Build.VERSION.SDK_INT >= AndroidVersions.API_23_ANDROID_6_0
                            ? getRoutedDeviceSummary(audioTrack)
                            : "n/a";
                    Ln.i("Speaker: pkts=" + receivedPacketCount.get()
                            + " queue=" + packetQueue.size() + "/" + PACKET_QUEUE_CAPACITY
                            + " dropped=" + droppedPacketCount.get() + " fed=" + inputFed
                            + " out=" + pcmWriter.outputGot + " outBytes=" + pcmWriter.outputBytes
                            + " writes=" + pcmWriter.audioTrackWrites + " writeBytes=" + pcmWriter.bytesWritten
                            + " shortWrites=" + pcmWriter.shortWrites + " writeErrors=" + pcmWriter.writeErrors
                            + " lastWrite=" + pcmWriter.lastWriteResult + "/" + pcmWriter.lastWriteSize
                            + " prefilled=" + pcmWriter.prefilledBytes + "/" + playbackStartThreshold
                            + " bufFrames=" + bufferSizeFrames + "/" + bufferCapacityFrames
                            + " underruns=" + underruns + " initState=" + audioTrack.getState()
                            + " playState=" + audioTrack.getPlayState()
                            + " headFrames=" + audioTrack.getPlaybackHeadPosition() + " route=" + route);
                    lastLogTime = now;
                }

                if (pendingPacket == null) {
                    try {
                        pendingPacket = packetQueue.poll(50, TimeUnit.MILLISECONDS);
                    } catch (InterruptedException e) {
                        if (stopped) {
                            return;
                        }
                        continue;
                    }
                    if (pendingPacket == null) {
                        if (receiverFinished.get()) {
                            if (queueDecoderEos(decoder)) {
                                drainDecoderEos(decoder, bufferInfo, pcmWriter);
                            }
                            if (!stopped && !pcmWriter.playbackStarted && pcmWriter.prefilledBytes > 0) {
                                playPrefilledPcm(audioTrack, pcmWriter.prefilledBytes, pcmFrameSize);
                            }
                            return;
                        }
                        continue;
                    }
                }

                int inputBufferId = decoder.dequeueInputBuffer(10000);
                if (inputBufferId < 0) {
                    continue;
                }

                ByteBuffer inputBuffer = decoder.getInputBuffer(inputBufferId);
                if (inputBuffer == null) {
                    throw new IOException("Speaker: MediaCodec input buffer unavailable");
                }
                inputBuffer.clear();
                inputBuffer.put(pendingPacket.data);
                inputBuffer.flip();
                decoder.queueInputBuffer(inputBufferId, 0, pendingPacket.data.length, pendingPacket.ptsUs, 0);
                inputFed++;
                pendingPacket = null;

                while (true) {
                    int outputBufferId = decoder.dequeueOutputBuffer(bufferInfo, 0);
                    if (outputBufferId < 0) {
                        break;
                    }
                    if (outputBufferId == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                        continue;
                    }

                    try {
                        pcmWriter.write(decoder.getOutputBuffer(outputBufferId), bufferInfo);
                    } finally {
                        decoder.releaseOutputBuffer(outputBufferId, false);
                    }
                }
            }
        } finally {
            if (audioTrack != null) {
                try {
                    audioTrack.stop();
                } catch (Exception ignored) {
                    // Ignore cleanup failures.
                }
                try {
                    audioTrack.release();
                } catch (Exception ignored) {
                    // Ignore cleanup failures.
                }
            }
            if (decoder != null) {
                if (decoderStarted) {
                    try {
                        decoder.stop();
                    } catch (Exception ignored) {
                        // Ignore cleanup failures.
                    }
                }
                try {
                    decoder.release();
                } catch (Exception ignored) {
                    // Ignore cleanup failures.
                }
            }
        }
    }

    private boolean queueDecoderEos(MediaCodec decoder) {
        for (int retry = 0; !stopped && retry < EOS_MAX_DEQUEUE_RETRIES; ++retry) {
            int inputBufferId = decoder.dequeueInputBuffer(EOS_DEQUEUE_TIMEOUT_US);
            if (inputBufferId >= 0) {
                decoder.queueInputBuffer(inputBufferId, 0, 0, 0, MediaCodec.BUFFER_FLAG_END_OF_STREAM);
                return true;
            }
        }
        if (!stopped) {
            Ln.w("Speaker: timed out waiting to queue decoder EOS");
        }
        return false;
    }

    private void drainDecoderEos(MediaCodec decoder, MediaCodec.BufferInfo bufferInfo, PcmWriter pcmWriter)
            throws IOException {
        for (int attempt = 0; !stopped && attempt < EOS_MAX_DEQUEUE_RETRIES; ++attempt) {
            int outputBufferId = decoder.dequeueOutputBuffer(bufferInfo, EOS_DEQUEUE_TIMEOUT_US);
            if (outputBufferId == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                continue;
            }
            if (outputBufferId < 0) {
                continue;
            }

            try {
                pcmWriter.write(decoder.getOutputBuffer(outputBufferId), bufferInfo);
            } finally {
                decoder.releaseOutputBuffer(outputBufferId, false);
            }
            if ((bufferInfo.flags & MediaCodec.BUFFER_FLAG_END_OF_STREAM) != 0) {
                return;
            }
        }
        if (!stopped) {
            Ln.w("Speaker: timed out draining decoder EOS");
        }
    }

    private void playPrefilledPcm(AudioTrack audioTrack, long prefilledBytes, int pcmFrameSize) {
        audioTrack.play();
        long playbackFrames = prefilledBytes / pcmFrameSize;
        while (!stopped && audioTrack.getPlayState() == AudioTrack.PLAYSTATE_PLAYING
                && audioTrack.getPlaybackHeadPosition() < playbackFrames) {
            try {
                Thread.sleep(10);
            } catch (InterruptedException e) {
                if (stopped) {
                    return;
                }
            }
        }
        Ln.i("Speaker: EOF prefill=" + prefilledBytes + " bytes playState=" + audioTrack.getPlayState()
                + " headFrames=" + audioTrack.getPlaybackHeadPosition());
    }

    private static final class PcmWriter {
        private final AudioTrack audioTrack;
        private final int playbackStartThreshold;

        private long outputGot;
        private long outputBytes;
        private long audioTrackWrites;
        private long bytesWritten;
        private long shortWrites;
        private long writeErrors;
        private int lastWriteResult;
        private int lastWriteSize;
        private long prefilledBytes;
        private boolean playbackStarted;

        PcmWriter(AudioTrack audioTrack, int playbackStartThreshold) {
            this.audioTrack = audioTrack;
            this.playbackStartThreshold = playbackStartThreshold;
        }

        void write(ByteBuffer outputBuffer, MediaCodec.BufferInfo bufferInfo) throws IOException {
            outputGot++;
            outputBytes += bufferInfo.size;
            if (outputBuffer == null || bufferInfo.size <= 0) {
                return;
            }

            byte[] pcmData = new byte[bufferInfo.size];
            outputBuffer.position(bufferInfo.offset);
            outputBuffer.get(pcmData, 0, bufferInfo.size);
            int writeOffset = 0;
            while (writeOffset < pcmData.length) {
                int writeSize = pcmData.length - writeOffset;
                if (!playbackStarted) {
                    writeSize = (int) Math.min(writeSize, playbackStartThreshold - prefilledBytes);
                }
                int written = audioTrack.write(pcmData, writeOffset, writeSize);
                audioTrackWrites++;
                lastWriteResult = written;
                lastWriteSize = writeSize;
                if (written <= 0) {
                    if (written < 0) {
                        writeErrors++;
                    } else {
                        shortWrites++;
                    }
                    throw new IOException("Speaker: AudioTrack.write() returned " + written);
                }
                bytesWritten += written;
                if (written < writeSize) {
                    shortWrites++;
                }
                writeOffset += written;
                if (!playbackStarted) {
                    prefilledBytes += written;
                    if (prefilledBytes >= playbackStartThreshold) {
                        audioTrack.play();
                        playbackStarted = true;
                        Ln.i("Speaker: playing after prefill=" + prefilledBytes
                                + "/" + playbackStartThreshold + " bytes");
                    }
                }
            }
        }
    }

    private void stopReceiving() {
        stopped = true;
        closeInputStream();
        Thread receiver = thread;
        if (receiver != null) {
            receiver.interrupt();
        }
    }

    private void closeInputStream() {
        try {
            inputStream.close();
        } catch (IOException ignored) {
            // Ignore close failures during shutdown.
        }
    }

    private void wakePlayback() {
        Thread worker = playbackThread;
        if (worker != null) {
            worker.interrupt();
        }
    }

    private void joinPlayback() {
        Thread worker = playbackThread;
        if (worker == null) {
            return;
        }
        boolean interrupted = false;
        while (true) {
            try {
                worker.join();
                break;
            } catch (InterruptedException e) {
                interrupted = true;
                stopped = true;
                closeInputStream();
                wakePlayback();
            }
        }
        if (interrupted) {
            Thread.currentThread().interrupt();
        }
    }

    private static final class Packet {
        private final byte[] data;
        private final long ptsUs;

        Packet(byte[] data, long ptsUs) {
            this.data = data;
            this.ptsUs = ptsUs;
        }
    }

    @TargetApi(AndroidVersions.API_23_ANDROID_6_0)
    private AudioTrack createAudioTrack(int bufferSize) {
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
                    .setBufferSizeInBytes(bufferSize)
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
                bufferSize,
                AudioTrack.MODE_STREAM);
        return track;
    }

    @TargetApi(AndroidVersions.API_23_ANDROID_6_0)
    private static int getBufferSizeInFrames(AudioTrack audioTrack) {
        return audioTrack.getBufferSizeInFrames();
    }

    @TargetApi(AndroidVersions.API_23_ANDROID_6_0)
    private static String getRoutedDeviceSummary(AudioTrack audioTrack) {
        AudioDeviceInfo routedDevice = audioTrack.getRoutedDevice();
        return routedDevice == null ? "none" : routedDevice.getType() + ":" + routedDevice.getProductName();
    }

    @TargetApi(AndroidVersions.API_24_ANDROID_7_0)
    private static int getBufferCapacityInFrames(AudioTrack audioTrack) {
        return audioTrack.getBufferCapacityInFrames();
    }

    @TargetApi(AndroidVersions.API_24_ANDROID_7_0)
    private static int getUnderrunCount(AudioTrack audioTrack) {
        return audioTrack.getUnderrunCount();
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
