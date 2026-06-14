package com.genymobile.scrcpy;

import com.genymobile.scrcpy.audio.AudioCapture;
import com.genymobile.scrcpy.audio.AudioCodec;
import com.genymobile.scrcpy.audio.AudioDirectCapture;
import com.genymobile.scrcpy.audio.AudioEncoder;
import com.genymobile.scrcpy.audio.AudioPlaybackCapture;
import com.genymobile.scrcpy.audio.AudioRawRecorder;
import com.genymobile.scrcpy.audio.AudioReversePlayback;
import com.genymobile.scrcpy.audio.AudioSource;
import com.genymobile.scrcpy.audio.WifiAudioEncoder;
import com.genymobile.scrcpy.control.ControlChannel;
import com.genymobile.scrcpy.control.Controller;
import com.genymobile.scrcpy.device.DesktopConnection;
import com.genymobile.scrcpy.device.Device;
import com.genymobile.scrcpy.device.Streamer;
import com.genymobile.scrcpy.device.WifiConnection;
import com.genymobile.scrcpy.device.WifiStreamer;
import com.genymobile.scrcpy.model.ConfigurationException;
import com.genymobile.scrcpy.model.NewDisplay;
import com.genymobile.scrcpy.model.StreamId;
import com.genymobile.scrcpy.opengl.OpenGLRunner;
import com.genymobile.scrcpy.util.Ln;
import com.genymobile.scrcpy.util.LogUtils;

import java.io.InputStream;
import java.io.OutputStream;
import com.genymobile.scrcpy.video.CameraCapture;
import com.genymobile.scrcpy.video.MultiCapture;
import com.genymobile.scrcpy.video.NewDisplayCapture;
import com.genymobile.scrcpy.video.ScreenCapture;
import com.genymobile.scrcpy.video.SurfaceCapture;
import com.genymobile.scrcpy.video.SurfaceEncoder;
import com.genymobile.scrcpy.video.VideoSource;
import com.genymobile.scrcpy.video.WifiSurfaceEncoder;

import android.annotation.SuppressLint;
import android.os.Build;
import android.os.Looper;

import java.io.FileDescriptor;
import java.io.IOException;
import android.system.Os;

import java.io.File;
import java.lang.reflect.Field;
import java.util.ArrayList;
import java.util.List;

public final class Server {

    public static final String SERVER_PATH;

    static {
        String[] classPaths = System.getProperty("java.class.path").split(File.pathSeparator);
        // By convention, scrcpy is always executed with the absolute path of scrcpy-server.jar as the first item in the classpath
        SERVER_PATH = classPaths[0];
    }

    private static class Completion {
        private int running;
        private int failed;
        private boolean daemon;

        Completion(int running, boolean daemon) {
            this.running = running;
            this.daemon = daemon;
            
            // Daemon mode: no global timeout.
            // Each stream independently reports fatal errors (e.g., TCP disconnect).
            // When all streams complete (either fatally or normally), the daemon
            // restarts automatically. If a stream is truly stuck, the TCP stack
            // will eventually timeout (typically 2+ minutes) and trigger a fatal error.
            // This ensures one healthy stream is never killed because another is stuck.
        }

        synchronized void addCompleted(boolean fatalError) {
            --running;
            if (fatalError) {
                ++failed;
                Ln.w("Stream failed (" + failed + "/" + (running + failed) + ") — other streams continue");
            }
            // Only quit when ALL streams are done (not on first failure)
            if (running == 0) {
                if (daemon) {
                    Ln.i("All streams completed — daemon mode will restart");
                } else if (failed > 0) {
                    Ln.w("All streams completed (" + failed + " had errors)");
                } else {
                    Ln.i("All streams completed successfully");
                }
                // Always quit Looper so code can continue
                Looper.getMainLooper().quitSafely();
            }
        }
    }

    private Server() {
        // not instantiable
    }

    private static void scrcpy(Options options) throws IOException, ConfigurationException {
        if (Build.VERSION.SDK_INT < AndroidVersions.API_31_ANDROID_12 && options.getVideoSource() == VideoSource.CAMERA) {
            Ln.e("Camera mirroring is not supported before Android 12");
            throw new ConfigurationException("Camera mirroring is not supported");
        }

        if (Build.VERSION.SDK_INT < AndroidVersions.API_29_ANDROID_10) {
            if (options.getNewDisplay() != null) {
                Ln.e("New virtual display is not supported before Android 10");
                throw new ConfigurationException("New virtual display is not supported");
            }
            if (options.getDisplayImePolicy() != -1) {
                Ln.e("Display IME policy is not supported before Android 10");
                throw new ConfigurationException("Display IME policy is not supported");
            }
        }

        CleanUp cleanUp = null;

        if (options.getCleanup()) {
            cleanUp = CleanUp.start(options);
        }

        int scid = options.getScid();
        boolean tunnelForward = options.isTunnelForward();
        boolean control = options.getControl();
        boolean video = options.getVideo();
        boolean audio = options.getAudio();
        boolean sendDummyByte = options.getSendDummyByte();
        boolean wifiMode = options.isWifiMode();
        boolean multistream = options.isMultistream();

        Workarounds.apply();

        if (wifiMode) {
            int wifiPort = options.getWifiPort();
            boolean cameraEnabled = multistream && video;
            boolean cameraDaemonEnabled = options.isCameraEnabled();
            boolean micEnabled = audio;
            boolean speakerEnabled = options.isSpeakerEnabled();
            boolean daemon = options.isDaemon();

            // Daemon mode: loop forever, restarting after streams complete.
            // Outer try-catch ensures no single exception kills the server process.
            while (true) {
                try {
                Ln.i("DEBUG: speaker=" + speakerEnabled + " video=" + video + " camera=" + (cameraEnabled || cameraDaemonEnabled) + " mic=" + micEnabled + " control=" + control + " daemon=" + daemon);
                Ln.i("Wi-Fi mode enabled, port " + wifiPort +
                     ", address " + WifiConnection.getLocalIpAddress());

                WifiConnection wifiConn;
                try {
                    // Fast path: speaker and/or camera daemon mode.
                    // Keep ports open permanently, accept connections in a loop.
                    // Speaker daemon runs on main thread (needs Looper for decoder callbacks).
                    // Camera daemon runs on background thread (posts callbacks to main Looper).
                    if ((speakerEnabled || cameraDaemonEnabled) && !video && !cameraEnabled && !micEnabled && !control && daemon) {
                    // Start mDNS advertisement so PC can discover this phone.
                    // Must run on main thread (ActivityThread.systemMain needs Looper).
                    new android.os.Handler(android.os.Looper.getMainLooper()).post(() -> {
                        startMdnsAdvertiser(wifiPort);
                    });

                        // Start camera daemon on background thread (if enabled)
                        Thread cameraThread = null;
                        if (cameraDaemonEnabled) {
                            int cameraPort = wifiPort + 1;
                            cameraThread = startCameraDaemon(cameraPort, options);
                        }

                        // Start speaker daemon on main thread (if enabled)
                        // This blocks the current thread via Looper.loop()
                        if (speakerEnabled) {
                            int speakerPort = wifiPort + 3;
                            runSpeakerDaemon(speakerPort);
                        } else if (cameraThread != null) {
                            // No speaker, just wait for camera thread
                            try { cameraThread.join(); } catch (InterruptedException ignored) {}
                        }
                        continue;
                    }

                    // Fast path: speaker-only (non-daemon) — one-shot.
                    if (speakerEnabled && !video && !cameraEnabled && !micEnabled && !control) {
                        wifiConn = WifiConnection.openSpeakerOnly(wifiPort + 3);
                    } else {
                        wifiConn = WifiConnection.open(video, cameraEnabled, micEnabled,
                                speakerEnabled, control, wifiPort);
                    }
                } catch (IOException e) {
                    Ln.e("Failed to open Wi-Fi connection: " + e.getMessage());
                    if (daemon) {
                        Ln.i("Daemon mode: retrying...");
                        try { Thread.sleep(500); } catch (InterruptedException ignored) {}
                        continue;
                    }
                    return;
                }

                try {
                    if (options.getSendDeviceMeta()) {
                        wifiConn.sendDeviceMeta(Device.getDeviceName());
                    }

                    Controller controller = null;

                    if (control) {
                        Ln.i("Control channel deferred to Phase 3 (Wi-Fi mode)");
                    }

                    List<AsyncProcessor> asyncProcessors = new ArrayList<>();

                    // === MIC STREAM ===
                    if (micEnabled) {
                        try {
                            OutputStream audioOutputStream = wifiConn.getAudioOutputStream();
                            if (audioOutputStream != null) {
                                AudioSource audioSource = options.getAudioSource();
                                AudioCapture audioCapture;
                                if (audioSource.isDirect()) {
                                    audioCapture = new AudioDirectCapture(audioSource);
                                } else {
                                    audioCapture = new AudioPlaybackCapture(options.getAudioDup());
                                }

                                StreamId audioStreamId = audioSource.isDirect()
                                        ? StreamId.MIC : StreamId.SPEAKER;
                                WifiStreamer audioStreamer = new WifiStreamer(audioOutputStream,
                                        options.getAudioCodec(), audioStreamId,
                                        options.getSendStreamMeta(), options.getSendFrameMeta());

                                WifiAudioEncoder audioEncoder = new WifiAudioEncoder(
                                        audioCapture, audioStreamer, options);
                                asyncProcessors.add(audioEncoder);
                                Ln.i("Wi-Fi mic stream enabled on port " + (wifiPort + 2));
                            }
                        } catch (Exception e) {
                            Ln.e("Failed to start mic stream: " + e.getMessage());
                        }
                    }

                    // === SPEAKER STREAM ===
                    if (speakerEnabled) {
                        try {
                            InputStream speakerInputStream = wifiConn.getSpeakerInputStream();
                            if (speakerInputStream != null) {
                                AudioReversePlayback reversePlayback = new AudioReversePlayback(
                                        speakerInputStream);
                                asyncProcessors.add(reversePlayback);
                                Ln.i("Wi-Fi speaker stream enabled on port " + (wifiPort + 3));
                            }
                        } catch (Exception e) {
                            Ln.e("Failed to start speaker stream: " + e.getMessage());
                        }
                    }

                    // === VIDEO STREAMS ===
                    if (video) {
                        try {
                            OutputStream videoOutputStream = wifiConn.getVideoOutputStream();
                            OutputStream cameraOutputStream = wifiConn.getCameraOutputStream();

                            if (multistream) {
                                Ln.i("Multi-stream mode: screen + camera enabled");

                                ScreenCapture screenCapture = new ScreenCapture(controller, options);
                                CameraCapture cameraCapture = new CameraCapture(options);

                                WifiStreamer screenStreamer = null;
                                WifiStreamer cameraStreamer = null;

                                if (videoOutputStream != null) {
                                    screenStreamer = new WifiStreamer(videoOutputStream,
                                            options.getVideoCodec(), StreamId.SCREEN,
                                            options.getSendStreamMeta(), options.getSendFrameMeta());
                                }
                                if (cameraOutputStream != null) {
                                    cameraStreamer = new WifiStreamer(cameraOutputStream,
                                            options.getVideoCodec(), StreamId.CAMERA,
                                            options.getSendStreamMeta(), options.getSendFrameMeta());
                                }

                                MultiCapture multiCapture = new MultiCapture(
                                        screenCapture, cameraCapture,
                                        screenStreamer, cameraStreamer,
                                        options);
                                List<AsyncProcessor> processors = multiCapture.createProcessors();
                                asyncProcessors.addAll(processors);

                                if (controller != null) {
                                    controller.setSurfaceCapture(screenCapture);
                                }
                            } else {
                                StreamId videoStreamId = options.getVideoSource() == VideoSource.CAMERA
                                        ? StreamId.CAMERA : StreamId.SCREEN;

                                if (videoOutputStream != null) {
                                    WifiStreamer wifiStreamer = new WifiStreamer(videoOutputStream,
                                            options.getVideoCodec(), videoStreamId,
                                            options.getSendStreamMeta(), options.getSendFrameMeta());

                                    SurfaceCapture surfaceCapture;
                                    if (options.getVideoSource() == VideoSource.DISPLAY) {
                                        NewDisplay newDisplay = options.getNewDisplay();
                                        if (newDisplay != null) {
                                            surfaceCapture = new NewDisplayCapture(controller, options);
                                        } else {
                                            int displayId = options.getDisplayId();
                                            if (displayId == Device.DISPLAY_ID_NONE) {
                                                displayId = 0;
                                            }
                                            surfaceCapture = new ScreenCapture(controller, options);
                                        }
                                    } else {
                                        surfaceCapture = new CameraCapture(options);
                                    }

                                    WifiSurfaceEncoder surfaceEncoder = new WifiSurfaceEncoder(
                                            surfaceCapture, wifiStreamer, options);
                                    asyncProcessors.add(surfaceEncoder);

                                    if (controller != null) {
                                        controller.setSurfaceCapture(surfaceCapture);
                                    }
                                } else {
                                    Ln.e("Failed to get video output stream");
                                }
                            }
                        } catch (Exception e) {
                            Ln.e("Failed to start video stream: " + e.getMessage());
                        }
                    }

                    // Start async processors
                    if (asyncProcessors.isEmpty()) {
                        Ln.w("No streams started — all streams failed or disabled");
                        if (daemon) {
                            wifiConn.close();
                            Ln.i("Daemon mode: retrying...");
                            try { Thread.sleep(500); } catch (InterruptedException ignored) {}
                            continue;
                        }
                        return;
                    }

                    Completion completion = new Completion(asyncProcessors.size(), daemon);
                    for (AsyncProcessor asyncProcessor : asyncProcessors) {
                        asyncProcessor.start((fatalError) -> {
                            completion.addCompleted(fatalError);
                        });
                    }

                    // Block until completion
                    Looper.loop();

                    wifiConn.shutdown();
                } catch (IOException e) {
                    Ln.e("Wi-Fi connection error: " + e.getMessage());
                } finally {
                    try {
                        wifiConn.close();
                    } catch (IOException e) {
                        // ignore
                    }
                }

                if (!daemon) {
                    return;
                }

                Ln.i("Daemon mode: streams completed, restarting...");
                try { Thread.sleep(500); } catch (InterruptedException ignored) {}
                } catch (Throwable t) {
                    Ln.e("Daemon loop error, restarting in 2 seconds: " + t.getMessage(), t);
                    try { Thread.sleep(2000); } catch (InterruptedException ignored) {}
                    // Loop continues — server never dies
                }
            }
        }

        // Original ADB mode
        List<AsyncProcessor> asyncProcessors = new ArrayList<>();
        DesktopConnection connection = DesktopConnection.open(scid, tunnelForward, video, audio, control, sendDummyByte);
        try {
            if (options.getSendDeviceMeta()) {
                connection.sendDeviceMeta(Device.getDeviceName());
            }

            Controller controller = null;

            if (control) {
                ControlChannel controlChannel = connection.getControlChannel();
                controller = new Controller(controlChannel, cleanUp, options);
                asyncProcessors.add(controller);
            }

            if (audio) {
                AudioCodec audioCodec = options.getAudioCodec();
                AudioSource audioSource = options.getAudioSource();
                AudioCapture audioCapture;
                if (audioSource.isDirect()) {
                    audioCapture = new AudioDirectCapture(audioSource);
                } else {
                    audioCapture = new AudioPlaybackCapture(options.getAudioDup());
                }

                // MIC-family sources → StreamId.MIC, OUTPUT/PLAYBACK → StreamId.SPEAKER
                StreamId audioStreamId = audioSource.isDirect() ? StreamId.MIC : StreamId.SPEAKER;
                Streamer audioStreamer = new Streamer(connection.getAudioFd(), audioCodec, audioStreamId,
                        options.getSendStreamMeta(), options.getSendFrameMeta());
                AsyncProcessor audioRecorder;
                if (audioCodec == AudioCodec.RAW) {
                    audioRecorder = new AudioRawRecorder(audioCapture, audioStreamer);
                } else {
                    audioRecorder = new AudioEncoder(audioCapture, audioStreamer, options);
                }
                asyncProcessors.add(audioRecorder);
            }

            if (video) {
                StreamId videoStreamId = options.getVideoSource() == VideoSource.CAMERA ? StreamId.CAMERA : StreamId.SCREEN;
                Streamer videoStreamer = new Streamer(connection.getVideoFd(), options.getVideoCodec(), videoStreamId,
                        options.getSendStreamMeta(), options.getSendFrameMeta());
                SurfaceCapture surfaceCapture;
                if (options.getVideoSource() == VideoSource.DISPLAY) {
                    NewDisplay newDisplay = options.getNewDisplay();
                    if (newDisplay != null) {
                        surfaceCapture = new NewDisplayCapture(controller, options);
                    } else {
                        assert options.getDisplayId() != Device.DISPLAY_ID_NONE;
                        surfaceCapture = new ScreenCapture(controller, options);
                    }
                } else {
                    surfaceCapture = new CameraCapture(options);
                }
                SurfaceEncoder surfaceEncoder = new SurfaceEncoder(surfaceCapture, videoStreamer, options);
                asyncProcessors.add(surfaceEncoder);

                if (controller != null) {
                    controller.setSurfaceCapture(surfaceCapture);
                }
            }

            Completion completion = new Completion(asyncProcessors.size(), false); // ADB mode doesn't need daemon
            for (AsyncProcessor asyncProcessor : asyncProcessors) {
                asyncProcessor.start((fatalError) -> {
                    completion.addCompleted(fatalError);
                });
            }

            Looper.loop(); // interrupted by the Completion implementation
        } finally {
            if (cleanUp != null) {
                cleanUp.interrupt();
            }
            for (AsyncProcessor asyncProcessor : asyncProcessors) {
                asyncProcessor.stop();
            }

            connection.shutdown();

            try {
                if (cleanUp != null) {
                    cleanUp.join();
                }
                for (AsyncProcessor asyncProcessor : asyncProcessors) {
                    asyncProcessor.join();
                }

                OpenGLRunner.shutdown();
            } catch (InterruptedException e) {
                // ignore
            }

            connection.close();
        }
    }

    private static void prepareMainLooper() {
        // Like Looper.prepareMainLooper(), but with quitAllowed set to true
        Looper.prepare();
        synchronized (Looper.class) {
            try {
                @SuppressLint("DiscouragedPrivateApi")
                Field field = Looper.class.getDeclaredField("sMainLooper");
                field.setAccessible(true);
                field.set(null, Looper.myLooper());
            } catch (ReflectiveOperationException e) {
                throw new AssertionError(e);
            }
        }
    }

    public static void main(String... args) {
        int status = 0;
        try {
            internalMain(args);
        } catch (Throwable t) {
            Ln.e(t.getMessage(), t);
            status = 1;
        } finally {
            // By default, the Java process exits when all non-daemon threads are terminated.
            // The Android SDK might start some non-daemon threads internally, preventing the scrcpy server to exit.
            // So force the process to exit explicitly.
            System.exit(status);
        }
    }

    private static void internalMain(String... args) throws Exception {
        Thread.UncaughtExceptionHandler defaultHandler = Thread.getDefaultUncaughtExceptionHandler();
        Thread.setDefaultUncaughtExceptionHandler((t, e) -> {
            Ln.e("Exception on thread " + t, e);
            if (defaultHandler != null) {
                defaultHandler.uncaughtException(t, e);
            }
        });

        dropRootPrivileges();

        prepareMainLooper();

        Options options = Options.parse(args);

        Ln.disableSystemStreams();
        Ln.initLogLevel(options.getLogLevel());

        Ln.i("Device: [" + Build.MANUFACTURER + "] " + Build.BRAND + " " + Build.MODEL + " (Android " + Build.VERSION.RELEASE + ")");

        if (options.getList()) {
            if (options.getCleanup()) {
                CleanUp.unlinkSelf();
            }

            if (options.getListEncoders()) {
                Ln.i(LogUtils.buildVideoEncoderListMessage());
                Ln.i(LogUtils.buildAudioEncoderListMessage());
            }
            if (options.getListDisplays()) {
                Ln.i(LogUtils.buildDisplayListMessage());
            }
            if (options.getListCameras() || options.getListCameraSizes()) {
                Workarounds.apply();
                Ln.i(LogUtils.buildCameraListMessage(options.getListCameraSizes()));
            }
            if (options.getListApps()) {
                Workarounds.apply();
                Ln.i("Processing Android apps... (this may take some time)");
                Ln.i(LogUtils.buildAppListMessage());
            }
            // Just print the requested data, do not mirror
            return;
        }

        try {
            scrcpy(options);
        } catch (ConfigurationException e) {
            // Do not print stack trace, a user-friendly error-message has already been logged
        }
    }

    @SuppressWarnings("deprecation")
    private static void dropRootPrivileges() {
        try {
            if (Os.getuid() == 0) {
                // Copy-paste does not work with root user
                // <https://github.com/Genymobile/scrcpy/issues/6224>
                Os.setuid(2000);
            }
        } catch (Exception e) {
            Ln.w("Cannot set UID", e);
        }
    }

    /**
     * Register mDNS service so PC can discover this phone automatically.
     * Called on the main thread (has Looper, required by NsdManager).
     * Also starts a UDP responder as fallback for networks where mDNS is blocked.
     */
    private static void startMdnsAdvertiser(int port) {
        // Register mDNS via Android NsdManager (standard approach, like AudioRelay)
        try {
            Class<?> atClass = Class.forName("android.app.ActivityThread");
            java.lang.reflect.Method systemMain = atClass.getMethod("systemMain");
            Object at = systemMain.invoke(null);
            java.lang.reflect.Method getSystemContext = atClass.getMethod("getSystemContext");
            android.content.Context ctx = (android.content.Context) getSystemContext.invoke(at);

            android.net.nsd.NsdServiceInfo info = new android.net.nsd.NsdServiceInfo();
            info.setServiceName("AllRelay");
            info.setServiceType("_allrelay._tcp");
            info.setPort(port);

            android.net.nsd.NsdManager mgr = (android.net.nsd.NsdManager)
                ctx.getSystemService(android.content.Context.NSD_SERVICE);
            if (mgr != null) {
                mgr.registerService(info, android.net.nsd.NsdManager.PROTOCOL_DNS_SD,
                    new android.net.nsd.NsdManager.RegistrationListener() {
                        public void onServiceRegistered(android.net.nsd.NsdServiceInfo i) {
                            Ln.i("mDNS: registered as " + i.getServiceName());
                        }
                        public void onRegistrationFailed(android.net.nsd.NsdServiceInfo i, int e) {
                            Ln.w("mDNS: registration failed, error=" + e);
                        }
                        public void onServiceUnregistered(android.net.nsd.NsdServiceInfo i) {}
                        public void onUnregistrationFailed(android.net.nsd.NsdServiceInfo i, int e) {}
                    });
            }
        } catch (Exception e) {
            Ln.w("mDNS: failed (non-fatal)", e);
        }

        // Also start UDP responder as reliable fallback
        startUdpResponder(port);
    }

    /** UDP discovery responder — listens for queries, responds with phone info. */
    private static void startUdpResponder(int port) {
        new Thread(() -> {
            try {
                java.net.DatagramSocket socket = new java.net.DatagramSocket(5009);
                socket.setBroadcast(true);
                byte[] buf = new byte[256];
                String response = "{\"name\":\"AllRelay\",\"port\":" + port + "}";
                Ln.i("Discovery: UDP responder on port 5009");
                while (true) {
                    try {
                        java.net.DatagramPacket packet = new java.net.DatagramPacket(buf, buf.length);
                        socket.receive(packet);
                        byte[] data = response.getBytes(java.nio.charset.StandardCharsets.UTF_8);
                        java.net.DatagramPacket reply = new java.net.DatagramPacket(
                            data, data.length, packet.getAddress(), packet.getPort());
                        socket.send(reply);
                    } catch (Exception ignored) {}
                }
            } catch (Exception e) {
                Ln.w("Discovery: UDP failed", e);
            }
        }, "udp-responder").start();
    }

    private static void runSpeakerDaemon(int speakerPort) {
        java.net.ServerSocket speakerServer = null;
        try {
            speakerServer = new java.net.ServerSocket();
            speakerServer.setReuseAddress(true);
            speakerServer.bind(new java.net.InetSocketAddress(speakerPort));
            Ln.i("Speaker daemon listening on port " + speakerPort);

            while (true) {
                java.net.Socket speakerSocket = null;
                try {
                    Ln.i("Waiting for speaker client on port " + speakerPort + "...");
                    speakerSocket = speakerServer.accept();
                    speakerSocket.setTcpNoDelay(true);
                    speakerSocket.getOutputStream().write(0xAB);
                    speakerSocket.getOutputStream().flush();
                    Ln.i("Wi-Fi speaker client connected from " + speakerSocket.getRemoteSocketAddress());

                    InputStream input = speakerSocket.getInputStream();
                    AudioReversePlayback reversePlayback = new AudioReversePlayback(input);
                    Completion completion = new Completion(1, true);
                    reversePlayback.start((fatalError) -> completion.addCompleted(fatalError));

                    Looper.loop(); // blocks until stream completes
                    Ln.i("Speaker stream ended, accepting next connection...");
                } catch (IOException e) {
                    Ln.e("Speaker accept error: " + e.getMessage());
                    if (speakerSocket != null) {
                        try { speakerSocket.close(); } catch (IOException ignored) {}
                    }
                    try { Thread.sleep(500); } catch (InterruptedException ignored) {}
                }
            }
        } catch (IOException e) {
            Ln.e("Speaker daemon: " + e.getMessage());
            try { Thread.sleep(500); } catch (InterruptedException ignored) {}
        } finally {
            if (speakerServer != null) {
                try { speakerServer.close(); } catch (IOException ignored) {}
            }
        }
    }

    /**
     * Start the camera daemon on a new background thread.
     * Listens on the given port, accepts connections, captures camera frames.
     * Returns the started thread (may be joined by caller).
     */
    private static Thread startCameraDaemon(int cameraPort, Options options) {
        Thread thread = new Thread(() -> {
            java.net.ServerSocket cameraServer = null;
            try {
                cameraServer = new java.net.ServerSocket();
                cameraServer.setReuseAddress(true);
                cameraServer.bind(new java.net.InetSocketAddress(cameraPort));
                Ln.i("Camera daemon listening on port " + cameraPort);

                while (true) {
                    java.net.Socket cameraSocket = null;
                    try {
                        Ln.i("Waiting for camera client on port " + cameraPort + "...");
                        cameraSocket = cameraServer.accept();
                        cameraSocket.setTcpNoDelay(true);
                        cameraSocket.getOutputStream().write(new byte[]{(byte)0xAB}); // dummy byte
                        cameraSocket.getOutputStream().flush();
                        Ln.i("Camera client connected from " + cameraSocket.getRemoteSocketAddress());

                        // Create CameraCapture on main looper
                        runCameraStream(cameraSocket, options);

                        Ln.i("Camera stream ended, accepting next connection...");
                    } catch (Exception e) {
                        Ln.e("Camera daemon stream error: " + e.getMessage());
                        if (cameraSocket != null) {
                            try { cameraSocket.close(); } catch (IOException ignored) {}
                        }
                        try { Thread.sleep(500); } catch (InterruptedException ignored) {}
                    }
                }
            } catch (IOException e) {
                Ln.e("Camera daemon bind failed: " + e.getMessage());
            } finally {
                if (cameraServer != null) {
                    try { cameraServer.close(); } catch (IOException ignored) {}
                }
            }
        }, "camera-daemon");
        thread.setDaemon(true);
        thread.start();
        return thread;
    }

    /**
     * Run a single camera stream session: capture camera via Camera2 API,
     * encode with MediaCodec H.264, stream to client socket via WifiStreamer.
     * Runs on the calling thread but posts CameraManager callbacks to main Looper.
     */
    private static void runCameraStream(java.net.Socket socket, Options options) throws Exception {
        OutputStream outputStream = socket.getOutputStream();

        CameraCapture cameraCapture = new CameraCapture(options);
        WifiStreamer cameraStreamer = new WifiStreamer(
                outputStream, options.getVideoCodec(), StreamId.CAMERA,
                options.getSendStreamMeta(), options.getSendFrameMeta());
        WifiSurfaceEncoder encoder = new WifiSurfaceEncoder(
                cameraCapture, cameraStreamer, options);

        // Start encoder, receive termination callback
        Completion completion = new Completion(1, true);
        encoder.start((fatalError) -> completion.addCompleted(fatalError));

        // Wait for stream to end (client disconnect or error)
        encoder.join();

        // Cleanup
        cameraCapture.stop();
        Ln.i("Camera stream completed");
    }
}
