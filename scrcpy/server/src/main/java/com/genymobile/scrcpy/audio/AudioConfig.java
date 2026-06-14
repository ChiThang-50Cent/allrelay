package com.genymobile.scrcpy.audio;

import android.media.AudioFormat;

public final class AudioConfig {
    public static final int SAMPLE_RATE = 48000;
    public static final int CHANNEL_CONFIG = AudioFormat.CHANNEL_IN_STEREO;
    public static final int CHANNELS = 2;
    public static final int CHANNEL_MASK = AudioFormat.CHANNEL_IN_LEFT | AudioFormat.CHANNEL_IN_RIGHT;
    public static final int ENCODING = AudioFormat.ENCODING_PCM_16BIT;
    public static final int BYTES_PER_SAMPLE = 2;

    // Never read more than 1024 samples, even if the buffer is bigger (that would increase latency).
    // A lower value is useless, since the system captures audio samples by blocks of 1024 (so for example if we read by blocks of 256 samples, we
    // receive 4 successive blocks without waiting, then we wait for the 4 next ones).
    public static final int MAX_READ_SIZE = maxReadSize(CHANNELS);

    private AudioConfig() {
        // Not instantiable
    }

    public static int maxReadSize(int channels) {
        return 1024 * channels * BYTES_PER_SAMPLE;
    }

    public static AudioFormat createAudioFormat() {
        return createAudioFormat(CHANNEL_CONFIG);
    }

    public static AudioFormat createAudioFormat(int channelMask) {
        AudioFormat.Builder builder = new AudioFormat.Builder();
        builder.setEncoding(ENCODING);
        builder.setSampleRate(SAMPLE_RATE);
        builder.setChannelMask(channelMask);
        return builder.build();
    }
}
