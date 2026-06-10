package com.genymobile.scrcpy.device;

import com.genymobile.scrcpy.util.Ln;

import java.io.Closeable;
import java.io.IOException;
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

    private static final int ACCEPT_TIMEOUT_MS = 30000; // 30 seconds
    private static final int DUMMY_BYTE = 0xAB;

    private final Socket videoSocket;
    private final Socket audioSocket;
    private final Socket controlSocket;

    private ServerSocket videoServerSocket;
    private ServerSocket audioServerSocket;
    private ServerSocket controlServerSocket;

    private WifiConnection(Socket videoSocket, Socket audioSocket,
                           Socket controlSocket) throws IOException {
        this.videoSocket = videoSocket;
        this.audioSocket = audioSocket;
        this.controlSocket = controlSocket;
    }

    /**
     * Open a Wi-Fi connection by listening on TCP ports.
     *
     * The PC client connects to these ports. Each accepted connection
     * sends a dummy byte so the client can detect a working connection.
     *
     * @param video    whether to listen for video stream
     * @param audio    whether to listen for audio stream
     * @param control  whether to listen for control channel
     * @param basePort starting port number (video=basePort, camera=basePort+1, etc.)
     * @return the established connection
     * @throws IOException if binding or accepting fails
     */
    public static WifiConnection open(boolean video, boolean audio, boolean control,
                                      int basePort) throws IOException {
        Socket videoSocket = null;
        Socket audioSocket = null;
        Socket controlSocket = null;

        ServerSocket videoServer = null;
        ServerSocket audioServer = null;
        ServerSocket controlServer = null;

        try {
            // Bind and listen on each port
            if (video) {
                videoServer = bindAndListen(basePort);
                Ln.d("Wi-Fi video listening on port " + basePort);
            }
            if (audio) {
                audioServer = bindAndListen(basePort + 2); // PORT_MIC
                Ln.d("Wi-Fi audio listening on port " + (basePort + 2));
            }
            if (control) {
                controlServer = bindAndListen(basePort + 4); // PORT_CONTROL
                Ln.d("Wi-Fi control listening on port " + (basePort + 4));
            }

            // Accept connections (blocking, with timeout)
            // Video is required, audio and control are optional
            if (videoServer != null) {
                videoSocket = acceptConnection(videoServer, "video");
            }
            // Audio is optional - don't fail if client doesn't connect
            if (audioServer != null) {
                try {
                    audioSocket = acceptConnection(audioServer, "audio");
                } catch (IOException e) {
                    Ln.w("Audio connection not established (client may have skipped it): " + e.getMessage());
                    // Continue without audio socket
                }
            }
            // Control is optional - don't fail if client doesn't connect
            if (controlServer != null) {
                try {
                    controlSocket = acceptConnection(controlServer, "control");
                } catch (IOException e) {
                    Ln.w("Control connection not established (client may have skipped it): " + e.getMessage());
                    // Continue without control socket
                }
            }

            // Close server sockets after all connections established
            closeServerSocket(videoServer);
            closeServerSocket(audioServer);
            closeServerSocket(controlServer);

            return new WifiConnection(videoSocket, audioSocket, controlSocket);

        } catch (IOException | RuntimeException e) {
            // Cleanup on failure
            closeSocket(videoSocket);
            closeSocket(audioSocket);
            closeSocket(controlSocket);
            closeServerSocket(videoServer);
            closeServerSocket(audioServer);
            closeServerSocket(controlServer);
            throw e;
        }
    }

    /**
     * Open with default AllRelay ports (5000-5004).
     */
    public static WifiConnection open(boolean video, boolean audio,
                                      boolean control) throws IOException {
        return open(video, audio, control, PORT_VIDEO);
    }

    private static ServerSocket bindAndListen(int port) throws IOException {
        ServerSocket serverSocket = new ServerSocket();
        serverSocket.setReuseAddress(true);
        serverSocket.setSoTimeout(ACCEPT_TIMEOUT_MS);
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

    private Socket getFirstSocket() {
        if (videoSocket != null) {
            return videoSocket;
        }
        if (audioSocket != null) {
            return audioSocket;
        }
        return controlSocket;
    }

    public void shutdown() throws IOException {
        if (videoSocket != null) {
            videoSocket.shutdownInput();
            videoSocket.shutdownOutput();
        }
        if (audioSocket != null) {
            audioSocket.shutdownInput();
            audioSocket.shutdownOutput();
        }
        if (controlSocket != null) {
            controlSocket.shutdownInput();
            controlSocket.shutdownOutput();
        }
    }

    @Override
    public void close() throws IOException {
        closeSocket(videoSocket);
        closeSocket(audioSocket);
        closeSocket(controlSocket);
    }

    /**
     * Get output stream for video data.
     * Use this to write video packets to the PC client.
     */
    public OutputStream getVideoOutputStream() throws IOException {
        return videoSocket != null ? videoSocket.getOutputStream() : null;
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
