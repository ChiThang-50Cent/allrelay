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

import java.io.IOException;
import java.io.InputStream;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;

/**
 * Receives Opus-encoded audio from the PC and plays it through the phone speaker.
 *
 * <p>This implements the <strong>reverse audio path</strong> (PC → phone speaker).
 * Data flows in the opposite direction compared to mic capture:
 * <ol>
 *   <li>PC encodes system audio to Opus</li>
 *   <li>PC sends RTP/UDP packets to port 5003</li>
 *   <li>This class reads from the socket input stream</li>
 *   <li>Parses the 16-byte AllRelay packet header</li>
 *   <li>Decodes Opus to PCM via MediaCodec</li>
 *   <li>Plays PCM through AudioTrack (AAudio preferred on Android 8+)</li>
 * </ol>
 */
public final class AudioReversePlayback implements AsyncProcessor {

    // Audio config for reverse playback (output to speaker)
    private static final int SAMPLE_RATE = AudioConfig.SAMPLE_RATE;
    private static final int CHANNELS = AudioConfig.CHANNELS;
    private static final int CHANNEL_CONFIG = AudioFormat.CHANNEL_OUT_STEREO;
    private static final int ENCODING = AudioConfig.ENCODING;

    // 16-byte header: [stream_id(4)] [pts+flags(8)] [size(4)]
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
        // Read config packet first (contains Opus codec info)
        byte[] headerBuf = new byte[HEADER_SIZE];

        // Create Opus decoder
        MediaCodec decoder = null;
        boolean decoderStarted = false;

        try {
            // Read the first packet header to confirm stream ID
        // and detect whether it's a config packet.
        int read = readFully(inputStream, headerBuf, 0, HEADER_SIZE);
        if (read < HEADER_SIZE) {
            Ln.w("Speaker: connection closed before receiving header");
            return;
        }

        int streamId = ByteBuffer.wrap(headerBuf, 0, 4).order(ByteOrder.BIG_ENDIAN).getInt();
        if (streamId != StreamId.SPEAKER.getId()) {
            Ln.e("Speaker: unexpected stream ID: 0x" + Integer.toHexString(streamId));
            return;
        }

        long firstPtsAndFlags = ByteBuffer.wrap(headerBuf, 4, 8).order(ByteOrder.BIG_ENDIAN).getLong();
        boolean firstIsConfig = (firstPtsAndFlags & PACKET_FLAG_CONFIG) != 0;
        int firstPacketSize = ByteBuffer.wrap(headerBuf, 12, 4).order(ByteOrder.BIG_ENDIAN).getInt();

        // Consume first packet payload (always, to stay aligned)
        byte[] firstPayload = null;
        if (firstPacketSize > 0) {
            firstPayload = new byte[firstPacketSize];
            readFully(inputStream, firstPayload, 0, firstPacketSize);
        }

        Ln.i("Speaker stream connected, config=" + firstIsConfig + " size=" + firstPacketSize);

            // Create MediaCodec decoder for Opus
            decoder = MediaCodec.createDecoderByType(AudioCodec.OPUS.getMimeType());

            MediaFormat format = MediaFormat.createAudioFormat(
                    AudioCodec.OPUS.getMimeType(), SAMPLE_RATE, CHANNELS);

            // If first packet is config (OpusHead), attach it as CSD-0
            // before configure() so the decoder has it during initialization.
            if (firstIsConfig && firstPacketSize > 0) {
                Ln.d("Speaker: feeding OpusHead config via configure, size=" + firstPacketSize);
                ByteBuffer csd0 = ByteBuffer.allocateDirect(firstPacketSize);
                csd0.put(firstPayload);
                csd0.flip();
                format.setByteBuffer("csd-0", csd0);
            }

            decoder.configure(format, null, null, 0);
            decoder.start();
            decoderStarted = true;

            // If first packet is audio (not config), feed it now
            if (!firstIsConfig && firstPayload != null) {
                Ln.d("Speaker: feeding first audio packet, size=" + firstPacketSize);
                int inputBufferId = decoder.dequeueInputBuffer(10000);
                if (inputBufferId >= 0) {
                    ByteBuffer inputBuffer = decoder.getInputBuffer(inputBufferId);
                    if (inputBuffer != null) {
                        inputBuffer.clear();
                        inputBuffer.put(firstPayload);
                        inputBuffer.flip();
                        long pts = firstPtsAndFlags & 0x1FFFFFFFFFFFFFL;
                        decoder.queueInputBuffer(inputBufferId, 0, firstPacketSize, pts, 0);
                    }
                }
            }

            // Create AudioTrack for playback
            int minBufferSize = AudioTrack.getMinBufferSize(SAMPLE_RATE, CHANNEL_CONFIG, ENCODING);
            audioTrack = createAudioTrack(minBufferSize);
            audioTrack.play();

            MediaCodec.BufferInfo bufferInfo = new MediaCodec.BufferInfo();
            ByteBuffer readBuffer = ByteBuffer.allocate(4096);

            // Main decode + playback loop
            while (!stopped && !Thread.currentThread().isInterrupted()) {
                // Read packet header
                int headerBytes = readFully(inputStream, headerBuf, 0, HEADER_SIZE);
                if (headerBytes < HEADER_SIZE) {
                    Ln.d("Speaker: connection closed");
                    break;
                }

                long ptsFlags = ByteBuffer.wrap(headerBuf, 4, 8).order(ByteOrder.BIG_ENDIAN).getLong();
                boolean config = (ptsFlags & PACKET_FLAG_CONFIG) != 0;
                int payloadSize = ByteBuffer.wrap(headerBuf, 12, 4).order(ByteOrder.BIG_ENDIAN).getInt();

                if (payloadSize <= 0 || payloadSize > 65536) {
                    Ln.w("Speaker: invalid payload size: " + payloadSize);
                    continue;
                }

                // Read payload
                if (readBuffer.capacity() < payloadSize) {
                    readBuffer = ByteBuffer.allocate(payloadSize);
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
                    // Config packets update decoder config
                    Ln.d("Speaker: received config packet, size=" + payloadSize);
                    continue;
                }

                // Feed Opus data to decoder
                int inputBufferId = decoder.dequeueInputBuffer(10000);
                if (inputBufferId < 0) {
                    continue;
                }

                ByteBuffer inputBuffer = decoder.getInputBuffer(inputBufferId);
                if (inputBuffer != null) {
                    inputBuffer.clear();
                    inputBuffer.put(readBuffer);
                    readBuffer.flip();
                    inputBuffer.flip();
                    long pts = ptsFlags & 0x1FFFFFFFFFFFFFL; // strip flags
                    decoder.queueInputBuffer(inputBufferId, 0, payloadSize, pts, 0);
                }

                // Get decoded PCM output
                int outputBufferId = decoder.dequeueOutputBuffer(bufferInfo, 10000);
                if (outputBufferId >= 0) {
                    ByteBuffer outputBuffer = decoder.getOutputBuffer(outputBufferId);
                    if (outputBuffer != null && bufferInfo.size > 0) {
                        byte[] pcmData = new byte[bufferInfo.size];
                        outputBuffer.position(bufferInfo.offset);
                        outputBuffer.get(pcmData, 0, bufferInfo.size);

                        // Write PCM to AudioTrack
                        audioTrack.write(pcmData, 0, bufferInfo.size);
                    }
                    decoder.releaseOutputBuffer(outputBufferId, false);
                }
            }
        } catch (Exception e) {
            Ln.e("Audio reverse playback error: " + e.getMessage());
            throw new IOException(e);
        } finally {
            if (audioTrack != null) {
                try {
                    audioTrack.stop();
                    audioTrack.release();
                } catch (Exception ignored) {
                }
            }
            if (decoder != null) {
                if (decoderStarted) {
                    try {
                        decoder.stop();
                    } catch (Exception ignored) {
                    }
                }
                try {
                    decoder.release();
                } catch (Exception ignored) {
                }
            }
            // Close input stream to signal client
            if (inputStream != null) {
                try {
                    inputStream.close();
                } catch (Exception ignored) {
                }
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
        // Fallback for older APIs
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

    /**
     * Read exactly len bytes from the InputStream, blocking if needed.
     */
    private static int readFully(InputStream in, byte[] buf, int off, int len) throws IOException {
        int totalRead = 0;
        while (totalRead < len) {
            int r = in.read(buf, off + totalRead, len - totalRead);
            if (r < 0) {
                break;
            }
            totalRead += r;
        }
        return totalRead;
    }
}
