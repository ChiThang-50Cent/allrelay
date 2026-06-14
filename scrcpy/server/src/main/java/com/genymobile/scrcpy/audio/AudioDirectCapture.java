package com.genymobile.scrcpy.audio;

import com.genymobile.scrcpy.AndroidVersions;
import com.genymobile.scrcpy.FakeContext;
import com.genymobile.scrcpy.Workarounds;
import com.genymobile.scrcpy.util.Ln;
import com.genymobile.scrcpy.wrappers.ServiceManager;

import android.annotation.SuppressLint;
import android.annotation.TargetApi;
import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.media.AudioManager;
import android.media.AudioRecord;
import android.media.MediaCodec;
import android.media.audiofx.AcousticEchoCanceler;
import android.media.audiofx.NoiseSuppressor;
import android.os.Build;
import android.os.SystemClock;

import java.nio.ByteBuffer;

public class AudioDirectCapture implements AudioCapture {

    private static final int SAMPLE_RATE = AudioConfig.SAMPLE_RATE;
    private static final int STEREO_CHANNEL_CONFIG = AudioConfig.CHANNEL_CONFIG;
    private static final int STEREO_CHANNELS = AudioConfig.CHANNELS;
    private static final int STEREO_CHANNEL_MASK = AudioConfig.CHANNEL_MASK;
    private static final int MONO_CHANNEL_CONFIG = android.media.AudioFormat.CHANNEL_IN_MONO;
    private static final int MONO_CHANNELS = 1;
    private static final int MONO_CHANNEL_MASK = android.media.AudioFormat.CHANNEL_IN_MONO;
    private static final int ENCODING = AudioConfig.ENCODING;
    private static final Object audioModeLock = new Object();
    private static int audioModeRefCount;
    private static Integer previousAudioMode;
    private static Boolean previousSpeakerphoneOn;

    private final int requestedAudioSource;
    private final boolean enableVoiceProcessing;
    private final int captureAudioSource;
    private final int channelConfig;
    private final int channelCount;
    private final int channelMask;

    private AudioRecord recorder;
    private AudioRecordReader reader;
    private AcousticEchoCanceler acousticEchoCanceler;
    private NoiseSuppressor noiseSuppressor;

    public AudioDirectCapture(AudioSource audioSource) {
        this(audioSource, false);
    }

    public AudioDirectCapture(AudioSource audioSource, boolean enableVoiceProcessing) {
        this.requestedAudioSource = audioSource.getDirectAudioSource();
        this.enableVoiceProcessing = enableVoiceProcessing;
        this.captureAudioSource = enableVoiceProcessing
                ? AudioSource.MIC_VOICE_COMMUNICATION.getDirectAudioSource()
                : requestedAudioSource;
        this.channelConfig = enableVoiceProcessing ? MONO_CHANNEL_CONFIG : STEREO_CHANNEL_CONFIG;
        this.channelCount = enableVoiceProcessing ? MONO_CHANNELS : STEREO_CHANNELS;
        this.channelMask = enableVoiceProcessing ? MONO_CHANNEL_MASK : STEREO_CHANNEL_MASK;
    }

    @TargetApi(AndroidVersions.API_23_ANDROID_6_0)
    @SuppressLint({"WrongConstant", "MissingPermission"})
    private AudioRecord createAudioRecord(int audioSource) {
        AudioRecord.Builder builder = new AudioRecord.Builder();
        if (Build.VERSION.SDK_INT >= AndroidVersions.API_31_ANDROID_12) {
            // On older APIs, Workarounds.fillAppInfo() must be called beforehand
            builder.setContext(FakeContext.get());
        }
        builder.setAudioSource(audioSource);
        builder.setAudioFormat(AudioConfig.createAudioFormat(channelConfig));
        int minBufferSize = AudioRecord.getMinBufferSize(SAMPLE_RATE, channelConfig, ENCODING);
        if (minBufferSize > 0) {
            // This buffer size does not impact latency
            builder.setBufferSizeInBytes(8 * minBufferSize);
        }

        return builder.build();
    }

    private static void startWorkaroundAndroid11() {
        // Android 11 requires Apps to be at foreground to record audio.
        // Normally, each App has its own user ID, so Android checks whether the requesting App has the user ID that's at the foreground.
        // But scrcpy server is NOT an App, it's a Java application started from Android shell, so it has the same user ID (2000) with Android
        // shell ("com.android.shell").
        // If there is an Activity from Android shell running at foreground, then the permission system will believe scrcpy is also in the
        // foreground.
        Intent intent = new Intent(Intent.ACTION_MAIN);
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        intent.addCategory(Intent.CATEGORY_LAUNCHER);
        intent.setComponent(new ComponentName(FakeContext.PACKAGE_NAME, "com.android.shell.HeapDumpActivity"));
        ServiceManager.getActivityManager().startActivity(intent);
    }

    private static void stopWorkaroundAndroid11() {
        ServiceManager.getActivityManager().forceStopPackage(FakeContext.PACKAGE_NAME);
    }

    private void tryStartRecording(int attempts, int delayMs) throws AudioCaptureException {
        while (attempts-- > 0) {
            // Wait for activity to start
            SystemClock.sleep(delayMs);
            try {
                startRecording();
                return; // it worked
            } catch (UnsupportedOperationException e) {
                if (attempts == 0) {
                    Ln.e("Failed to start audio capture");
                    Ln.e("On Android 11, audio capture must be started in the foreground, make sure that the device is unlocked when starting "
                            + "scrcpy.");
                    throw new AudioCaptureException();
                } else {
                    Ln.d("Failed to start audio capture, retrying...");
                }
            }
        }
    }

    private void startRecording() throws AudioCaptureException {
        try {
            recorder = createAudioRecord(captureAudioSource);
        } catch (NullPointerException e) {
            // Creating an AudioRecord using an AudioRecord.Builder does not work on Vivo phones:
            // - <https://github.com/Genymobile/scrcpy/issues/3805>
            // - <https://github.com/Genymobile/scrcpy/pull/3862>
            recorder = Workarounds.createAudioRecord(captureAudioSource, SAMPLE_RATE, channelConfig, channelCount, channelMask, ENCODING);
        }
        configureCommunicationMode();
        configureVoiceProcessing();
        recorder.startRecording();
        reader = new AudioRecordReader(recorder, channelCount, getMaxReadSize());
        Ln.i("Mic capture started: source=" + captureAudioSource
                + " requested_source=" + requestedAudioSource
                + " channels=" + channelCount
                + " voice_processing=" + enableVoiceProcessing);
    }

    @Override
    public void checkCompatibility() throws AudioCaptureException {
        if (Build.VERSION.SDK_INT < AndroidVersions.API_30_ANDROID_11) {
            Ln.w("Audio disabled: it is not supported before Android 11");
            throw new AudioCaptureException();
        }
    }

    @Override
    public void start() throws AudioCaptureException {
        if (Build.VERSION.SDK_INT == AndroidVersions.API_30_ANDROID_11) {
            startWorkaroundAndroid11();
            try {
                tryStartRecording(5, 100);
            } finally {
                stopWorkaroundAndroid11();
            }
        } else {
            startRecording();
        }
    }

    private AudioManager getAudioManager() {
        Context baseContext = FakeContext.get().getBaseContext();
        if (baseContext == null) {
            return null;
        }
        try {
            java.lang.reflect.Constructor<AudioManager> ctor = AudioManager.class.getDeclaredConstructor(Context.class);
            ctor.setAccessible(true);
            return ctor.newInstance(baseContext);
        } catch (Exception e) {
            Object service = baseContext.getSystemService(Context.AUDIO_SERVICE);
            if (service instanceof AudioManager) {
                return (AudioManager) service;
            }
            Ln.w("Mic AudioManager init failed: " + e.getMessage());
            return null;
        }
    }

    private void configureCommunicationMode() {
        if (!enableVoiceProcessing) {
            return;
        }

        AudioManager audioManager = getAudioManager();
        if (audioManager == null) {
            Ln.w("Mic communication mode unavailable: no AudioManager");
            return;
        }
        synchronized (audioModeLock) {
            if (audioModeRefCount == 0) {
                previousAudioMode = audioManager.getMode();
                previousSpeakerphoneOn = audioManager.isSpeakerphoneOn();
                try {
                    audioManager.setMode(AudioManager.MODE_IN_COMMUNICATION);
                    audioManager.setSpeakerphoneOn(true);
                    Ln.i("Mic communication mode enabled: mode=" + audioManager.getMode()
                            + " speakerphone=" + audioManager.isSpeakerphoneOn());
                } catch (RuntimeException e) {
                    Ln.w("Mic communication mode enable failed: " + e.getMessage());
                }
            }
            audioModeRefCount++;
        }
    }

    private void restoreCommunicationMode() {
        if (!enableVoiceProcessing) {
            return;
        }

        AudioManager audioManager = getAudioManager();
        if (audioManager == null) {
            return;
        }
        synchronized (audioModeLock) {
            if (audioModeRefCount <= 0) {
                audioModeRefCount = 0;
                return;
            }
            audioModeRefCount--;
            if (audioModeRefCount == 0) {
                try {
                    if (previousSpeakerphoneOn != null) {
                        audioManager.setSpeakerphoneOn(previousSpeakerphoneOn);
                    }
                    if (previousAudioMode != null) {
                        audioManager.setMode(previousAudioMode);
                    }
                    Ln.i("Mic communication mode restored: mode=" + audioManager.getMode()
                            + " speakerphone=" + audioManager.isSpeakerphoneOn());
                } catch (RuntimeException e) {
                    Ln.w("Mic communication mode restore failed: " + e.getMessage());
                } finally {
                    previousAudioMode = null;
                    previousSpeakerphoneOn = null;
                }
            }
        }
    }

    private void configureVoiceProcessing() {
        if (!enableVoiceProcessing || recorder == null) {
            return;
        }

        int sessionId = recorder.getAudioSessionId();
        boolean aecEnabled = false;
        boolean nsEnabled = false;

        if (AcousticEchoCanceler.isAvailable()) {
            acousticEchoCanceler = AcousticEchoCanceler.create(sessionId);
            if (acousticEchoCanceler != null) {
                try {
                    acousticEchoCanceler.setEnabled(true);
                    aecEnabled = acousticEchoCanceler.getEnabled();
                } catch (IllegalArgumentException | UnsupportedOperationException e) {
                    Ln.w("Mic AEC enable failed: " + e.getMessage());
                }
            }
        }

        if (NoiseSuppressor.isAvailable()) {
            noiseSuppressor = NoiseSuppressor.create(sessionId);
            if (noiseSuppressor != null) {
                try {
                    noiseSuppressor.setEnabled(true);
                    nsEnabled = noiseSuppressor.getEnabled();
                } catch (IllegalArgumentException | UnsupportedOperationException e) {
                    Ln.w("Mic NS enable failed: " + e.getMessage());
                }
            }
        }

        Ln.i("Mic preprocessing: aec=" + aecEnabled + " ns=" + nsEnabled + " session=" + sessionId);
    }

    private void releaseVoiceProcessing() {
        if (acousticEchoCanceler != null) {
            try {
                acousticEchoCanceler.release();
            } catch (Exception ignored) {}
            acousticEchoCanceler = null;
        }
        if (noiseSuppressor != null) {
            try {
                noiseSuppressor.release();
            } catch (Exception ignored) {}
            noiseSuppressor = null;
        }
    }

    @Override
    public void stop() {
        releaseVoiceProcessing();
        if (recorder != null) {
            // Will call .stop() if necessary, without throwing an IllegalStateException
            recorder.release();
            recorder = null;
        }
        restoreCommunicationMode();
    }

    @Override
    public int getChannelCount() {
        return channelCount;
    }

    @Override
    @TargetApi(AndroidVersions.API_24_ANDROID_7_0)
    public int read(ByteBuffer outDirectBuffer, MediaCodec.BufferInfo outBufferInfo) {
        return reader.read(outDirectBuffer, outBufferInfo);
    }
}
