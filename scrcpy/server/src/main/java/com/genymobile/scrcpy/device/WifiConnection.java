package com.genymobile.scrcpy.device;

import com.genymobile.scrcpy.util.Ln;

import java.io.Closeable;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.SocketTimeoutException;
import java.nio.charset.StandardCharsets;

/**
 * Direct Wi-Fi connection for AllRelay.
 *
 * Unlike DesktopConnection which uses ADB tunnel (LocalSocket),
 * this class listens on a TCP port and accepts connections from the PC client.
 *
 * Port allocation:
 *   5000 - video (screen/camera)
 *   5001 - camera (when multi-stream)
 *   5002 - audio (mic)
 *   5003 - audio (speaker, reverse direction)
 *   5004 - control
 *
 * Note: This class uses raw TCP sockets. For control channel,
 * we use a simple socket-based approach instead of ControlChannel
 * (which requires LocalSocket/ADB).
 */
public final class WifiConnection implements Closeable {

    private static final int DEVICE_NAME_FIELD_LENGTH = 64;

    // Default ports for AllRelay streams
    public static final int PORT_VIDEO = 5000;
    public static final int PORT_CAMERA = 5001;
    public static final int PORT_MIC = 5002;
    public static final int PORT_SPEAKER = 5003;
    public static final int PORT_CONTROL = 5004;

    private static final int ACCEPT_TIMEOUT_MS = 30000; // 30 seconds (mandatory: video, control)
    private static final int ACCEPT_TIMEOUT_OPTIONAL_MS = 3000; // 3 seconds (optional: camera, mic, speaker)
    private static final int DUMMY_BYTE = 0xAB;

    private final Socket videoSocket;
    private final Socket cameraSocket;
    private final Socket audioSocket;
    private final Socket speakerSocket;
    private final Socket controlSocket;

    private ServerSocket videoServerSocket;
    private ServerSocket cameraServerSocket;
    private ServerSocket audioServerSocket;
    private ServerSocket speakerServerSocket;
    private ServerSocket controlServerSocket;

    private WifiConnection(Socket videoSocket, Socket cameraSocket,
                           Socket audioSocket, Socket speakerSocket,
                           Socket controlSocket) throws IOException {
        this.videoSocket = videoSocket;
        this.cameraSocket = cameraSocket;
        this.audioSocket = audioSocket;
        this.speakerSocket = speakerSocket;
        this.controlSocket = controlSocket;
    }

    /**
     * Open a Wi-Fi connection by listening on TCP ports.
     *
     * The PC client connects to these ports. Each accepted connection
     * sends a dummy byte so the client can detect a working connection.
     *
     * @param video    whether to listen for video (screen) stream
     * @param camera   whether to listen for camera stream
     * @param audio    whether to listen for mic stream (outbound)
     * @param speaker  whether to listen for speaker stream (inbound, PC→phone)
     * @param control  whether to listen for control channel
     * @param basePort starting port number (video=basePort, camera=basePort+1, mic=+2, spk=+3, ctrl=+4)
     * @return the established connection
     * @throws IOException if binding or accepting fails
     */
    public static WifiConnection open(boolean video, boolean camera, boolean audio,
                                      boolean speaker, boolean control,
                                      int basePort) throws IOException {
        Socket videoSocket = null;
        Socket cameraSocket = null;
        Socket audioSocket = null;
        Socket speakerSocket = null;
        Socket controlSocket = null;

        ServerSocket videoServer = null;
        ServerSocket cameraServer = null;
        ServerSocket audioServer = null;
        ServerSocket speakerServer = null;
        ServerSocket controlServer = null;

        try {
            // Bind and listen on each port
            if (video) {
                videoServer = bindAndListen(basePort);
                Ln.d("Wi-Fi video listening on port " + basePort);
            }
            if (camera) {
                cameraServer = bindAndListenWithTimeout(basePort + 1, ACCEPT_TIMEOUT_OPTIONAL_MS); // PORT_CAMERA
                Ln.d("Wi-Fi camera listening on port " + (basePort + 1));
            }
            if (audio) {
                audioServer = bindAndListenWithTimeout(basePort + 2, ACCEPT_TIMEOUT_OPTIONAL_MS); // PORT_MIC
                Ln.d("Wi-Fi audio (mic) listening on port " + (basePort + 2));
            }
            if (speaker) {
                speakerServer = bindAndListenWithTimeout(basePort + 3, ACCEPT_TIMEOUT_OPTIONAL_MS); // PORT_SPEAKER
                Ln.d("Wi-Fi speaker listening on port " + (basePort + 3));
            }
            if (control) {
                controlServer = bindAndListen(basePort + 4); // PORT_CONTROL (long timeout — mandatory)
                Ln.d("Wi-Fi control listening on port " + (basePort + 4));
            }

            // Accept connections in parallel to avoid blocking the control
            // port behind optional camera/audio/speaker timeouts.
            final Socket[] acceptedVideo = {null};
            final Socket[] acceptedCamera = {null};
            final Socket[] acceptedAudio = {null};
            final Socket[] acceptedSpeaker = {null};
            final Socket[] acceptedControl = {null};

            // Capture references as final for lambda use
            final ServerSocket fVideoServer = videoServer;
            final ServerSocket fCameraServer = cameraServer;
            final ServerSocket fAudioServer = audioServer;
            final ServerSocket fSpeakerServer = speakerServer;
            final ServerSocket fControlServer = controlServer;

            java.util.List<Thread> acceptThreads = new java.util.ArrayList<>();

            if (fVideoServer != null) {
                Thread t = new Thread(() -> {
                    try {
                        acceptedVideo[0] = acceptConnection(fVideoServer, "video");
                        // Send device name immediately after video connection,
                        // before other accept threads complete (avoids deadlock
                        // where client waits for device name but server waits
                        // for control/mic/speaker connections).
                        sendDeviceMetaAsync(acceptedVideo[0]);
                    } catch (IOException ignored) {}
                }, "accept-video");
                acceptThreads.add(t);
                t.start();
            }
            if (fCameraServer != null) {
                Thread t = new Thread(() -> {
                    try { acceptedCamera[0] = acceptConnection(fCameraServer, "camera"); }
                    catch (IOException ignored) {}
                }, "accept-camera");
                acceptThreads.add(t);
                t.start();
            }
            if (fAudioServer != null) {
                Thread t = new Thread(() -> {
                    try { acceptedAudio[0] = acceptConnection(fAudioServer, "audio"); }
                    catch (IOException ignored) {}
                }, "accept-audio");
                acceptThreads.add(t);
                t.start();
            }
            if (fSpeakerServer != null) {
                Thread t = new Thread(() -> {
                    try { acceptedSpeaker[0] = acceptConnection(fSpeakerServer, "speaker"); }
                    catch (IOException ignored) {}
                }, "accept-speaker");
                acceptThreads.add(t);
                t.start();
            }
            if (fControlServer != null) {
                Thread t = new Thread(() -> {
                    try { acceptedControl[0] = acceptConnection(fControlServer, "control"); }
                    catch (IOException ignored) {}
                }, "accept-control");
                acceptThreads.add(t);
                t.start();
            }

            // Wait for mandatory accept threads (video, control) to complete.
            // Optional threads (camera, audio, speaker) join with a short timeout
            // to avoid blocking startup when the client skips those ports.
            final long OPTIONAL_TIMEOUT_MS = 4000;

            for (Thread t : acceptThreads) {
                String tname = t.getName();
                boolean isOptional = tname.startsWith("accept-camera")
                        || tname.startsWith("accept-audio")
                        || tname.startsWith("accept-speaker");
                try {
                    if (isOptional) {
                        t.join(OPTIONAL_TIMEOUT_MS);
                    } else {
                        t.join();
                    }
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                }
            }

            videoSocket = acceptedVideo[0];
            cameraSocket = acceptedCamera[0];
            audioSocket = acceptedAudio[0];
            speakerSocket = acceptedSpeaker[0];
            controlSocket = acceptedControl[0];

            // Close server sockets after all connections established
            closeServerSocket(videoServer);
            closeServerSocket(cameraServer);
            closeServerSocket(audioServer);
            closeServerSocket(speakerServer);
            closeServerSocket(controlServer);

            return new WifiConnection(videoSocket, cameraSocket, audioSocket,
                                     speakerSocket, controlSocket);

        } catch (IOException | RuntimeException e) {
            // Cleanup on failure
            closeSocket(videoSocket);
            closeSocket(cameraSocket);
            closeSocket(audioSocket);
            closeSocket(speakerSocket);
            closeSocket(controlSocket);
            closeServerSocket(videoServer);
            closeServerSocket(cameraServer);
            closeServerSocket(audioServer);
            closeServerSocket(speakerServer);
            closeServerSocket(controlServer);
            throw e;
        }
    }

    /**
     * Open with default AllRelay ports (5000-5004).
     */
    public static WifiConnection open(boolean video, boolean audio,
                                      boolean control) throws IOException {
        return open(video, false, audio, false, control, PORT_VIDEO);
    }

    private static ServerSocket bindAndListen(int port) throws IOException {
        return bindAndListenWithTimeout(port, ACCEPT_TIMEOUT_MS);
    }

    private static ServerSocket bindAndListenWithTimeout(int port, int timeoutMs) throws IOException {
        ServerSocket serverSocket = new ServerSocket();
        serverSocket.setReuseAddress(true);
        serverSocket.setSoTimeout(timeoutMs);
        serverSocket.bind(new InetSocketAddress(port), 1);
        return serverSocket;
    }

    private static Socket acceptConnection(ServerSocket serverSocket,
                                           String streamName) throws IOException {
        try {
            Socket socket = serverSocket.accept();
            socket.setTcpNoDelay(true);
            socket.setSendBufferSize(256 * 1024); // 256KB buffer

            // Send a dummy byte so the client can detect a working connection.
            // The client's connect_and_read_byte() reads 1 byte after connecting.
            socket.getOutputStream().write(DUMMY_BYTE);
            socket.getOutputStream().flush();

            Ln.i("Wi-Fi " + streamName + " client connected from "
                 + socket.getRemoteSocketAddress());
            return socket;
        } catch (SocketTimeoutException e) {
            throw new IOException("Timeout waiting for " + streamName
                                  + " client connection", e);
        }
    }

    private static void closeSocket(Socket socket) {
        if (socket != null) {
            try {
                socket.close();
            } catch (IOException e) {
                // ignore
            }
        }
    }

    private static void closeServerSocket(ServerSocket serverSocket) {
        if (serverSocket != null) {
            try {
                serverSocket.close();
            } catch (IOException e) {
                // ignore
            }
        }
    }

    public void sendDeviceMeta(String deviceName) throws IOException {
        byte[] buffer = new byte[DEVICE_NAME_FIELD_LENGTH];
        byte[] nameBytes = deviceName.getBytes(StandardCharsets.UTF_8);
        int len = Math.min(nameBytes.length, DEVICE_NAME_FIELD_LENGTH - 1);
        System.arraycopy(nameBytes, 0, buffer, 0, len);
        // buffer is zero-initialized, so null terminator is implicit

        Socket socket = getFirstSocket();
        if (socket != null) {
            OutputStream out = socket.getOutputStream();
            out.write(buffer);
            out.flush();
        }
    }

    /**
     * Send device name to a specific socket (used by parallel accept thread).
     */
    private static void sendDeviceMetaAsync(Socket socket) {
        if (socket == null) return;
        try {
            byte[] buffer = new byte[DEVICE_NAME_FIELD_LENGTH];
            byte[] nameBytes = Device.getDeviceName().getBytes(StandardCharsets.UTF_8);
            int len = Math.min(nameBytes.length, DEVICE_NAME_FIELD_LENGTH - 1);
            System.arraycopy(nameBytes, 0, buffer, 0, len);
            OutputStream out = socket.getOutputStream();
            out.write(buffer);
            out.flush();
        } catch (IOException e) {
            Ln.w("Failed to send device name: " + e.getMessage());
        }
    }

    private Socket getFirstSocket() {
        if (videoSocket != null) {
            return videoSocket;
        }
        if (cameraSocket != null) {
            return cameraSocket;
        }
        if (audioSocket != null) {
            return audioSocket;
        }
        if (speakerSocket != null) {
            return speakerSocket;
        }
        return controlSocket;
    }

    public void shutdown() throws IOException {
        if (videoSocket != null) {
            videoSocket.shutdownInput();
            videoSocket.shutdownOutput();
        }
        if (cameraSocket != null) {
            cameraSocket.shutdownInput();
            cameraSocket.shutdownOutput();
        }
        if (audioSocket != null) {
            audioSocket.shutdownInput();
            audioSocket.shutdownOutput();
        }
        if (speakerSocket != null) {
            speakerSocket.shutdownInput();
            speakerSocket.shutdownOutput();
        }
        if (controlSocket != null) {
            controlSocket.shutdownInput();
            controlSocket.shutdownOutput();
        }
    }

    @Override
    public void close() throws IOException {
        closeSocket(videoSocket);
        closeSocket(cameraSocket);
        closeSocket(audioSocket);
        closeSocket(speakerSocket);
        closeSocket(controlSocket);
    }

    /**
     * Get output stream for video (screen) data.
     * Use this to write screen video packets to the PC client.
     */
    public OutputStream getVideoOutputStream() throws IOException {
        return videoSocket != null ? videoSocket.getOutputStream() : null;
    }

    /**
     * Get output stream for camera data.
     * Use this to write camera video packets to the PC client.
     */
    public OutputStream getCameraOutputStream() throws IOException {
        return cameraSocket != null ? cameraSocket.getOutputStream() : null;
    }

    /**
     * Get input stream for speaker data (PC → phone reverse audio).
     * Use this to read Opus-encoded speaker audio from the PC client.
     */
    public InputStream getSpeakerInputStream() throws IOException {
        return speakerSocket != null ? speakerSocket.getInputStream() : null;
    }

    /**
     * Get output stream for audio data.
     * Use this to write audio packets to the PC client.
     */
    public OutputStream getAudioOutputStream() throws IOException {
        return audioSocket != null ? audioSocket.getOutputStream() : null;
    }

    /**
     * Get the control socket for bidirectional control communication.
     */
    public Socket getControlSocket() {
        return controlSocket;
    }

    /**
     * Get the local IP address that clients should connect to.
     * This is a convenience method for display/logging purposes.
     */
    public static String getLocalIpAddress() {
        try {
            java.util.Enumeration<java.net.NetworkInterface> interfaces =
                java.net.NetworkInterface.getNetworkInterfaces();
            while (interfaces.hasMoreElements()) {
                java.net.NetworkInterface iface = interfaces.nextElement();
                if (iface.isLoopback() || !iface.isUp()) continue;

                java.util.Enumeration<java.net.InetAddress> addresses =
                    iface.getInetAddresses();
                while (addresses.hasMoreElements()) {
                    java.net.InetAddress addr = addresses.nextElement();
                    if (addr instanceof java.net.Inet4Address
                        && !addr.isLoopbackAddress()) {
                        return addr.getHostAddress();
                    }
                }
            }
        } catch (Exception e) {
            // ignore
        }
        return "unknown";
    }
}
