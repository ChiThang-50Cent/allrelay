package com.genymobile.scrcpy.audio;

import android.media.MediaCodec;

import java.nio.ByteBuffer;

public interface AudioCapture {
    void checkCompatibility() throws AudioCaptureException;
    void start() throws AudioCaptureException;
    void stop();

    default int getSampleRate() {
        return AudioConfig.SAMPLE_RATE;
    }

    default int getChannelCount() {
        return AudioConfig.CHANNELS;
    }

    default int getMaxReadSize() {
        return AudioConfig.maxReadSize(getChannelCount());
    }

    /**
     * Read a chunk of PCM samples.
     *
     * @param outDirectBuffer The target buffer
     * @param outBufferInfo The info to provide to MediaCodec
     * @return the number of bytes actually read.
     */
    int read(ByteBuffer outDirectBuffer, MediaCodec.BufferInfo outBufferInfo);
}
