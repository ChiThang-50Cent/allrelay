package com.genymobile.scrcpy.net;

import android.content.Context;
import android.net.nsd.NsdManager;
import android.net.nsd.NsdServiceInfo;
import android.os.Build;

import com.genymobile.scrcpy.util.Ln;

/**
 * mDNS service broadcaster for AllRelay.
 *
 * Broadcasts the AllRelay service on the local network so that
 * PC clients can discover the phone without knowing its IP address.
 *
 * Service type: _allrelay._tcp
 * Service name: AllRelay-<deviceName>
 * TXT records:
 *   - version: protocol version
 *   - device: device model name
 *   - port: base TCP port
 *   - streams: available streams (screen,camera,mic,speaker)
 */
public final class MdnsService {

    public static final String SERVICE_TYPE = "_allrelay._tcp.";
    public static final String SERVICE_NAME_PREFIX = "AllRelay-";

    private static final String PROTOCOL_VERSION = "1";

    private final NsdManager nsdManager;
    private final String serviceName;
    private final int port;
    private final String deviceName;

    private boolean registered;
    private NsdManager.RegistrationListener registrationListener;

    /**
     * Create a new mDNS service broadcaster.
     *
     * @param context    Android context
     * @param deviceName device name (e.g., "Pixel 7")
     * @param port       base TCP port for AllRelay connections
     */
    public MdnsService(Context context, String deviceName, int port) {
        this.nsdManager = (NsdManager) context.getSystemService(Context.NSD_SERVICE);
        this.deviceName = deviceName;
        this.port = port;
        this.serviceName = SERVICE_NAME_PREFIX + sanitizeServiceName(deviceName);
    }

    /**
     * Start broadcasting the AllRelay service on the network.
     *
     * @param callback optional callback for registration events
     */
    public void start(RegistrationCallback callback) {
        if (registered) {
            Ln.w("mDNS service already registered");
            return;
        }

        NsdServiceInfo serviceInfo = new NsdServiceInfo();
        serviceInfo.setServiceName(serviceName);
        serviceInfo.setServiceType(SERVICE_TYPE);
        serviceInfo.setPort(port);

        // Add TXT records with device information
        try {
            serviceInfo.setAttribute("version", PROTOCOL_VERSION);
            serviceInfo.setAttribute("device", Build.MODEL);
            serviceInfo.setAttribute("port", String.valueOf(port));
            serviceInfo.setAttribute("streams", "screen,camera,mic,speaker");
            serviceInfo.setAttribute("manufacturer", Build.MANUFACTURER);
        } catch (IllegalArgumentException e) {
            Ln.w("Could not set TXT record attribute", e);
        }

        registrationListener = new NsdManager.RegistrationListener() {
            @Override
            public void onServiceRegistered(NsdServiceInfo info) {
                String registeredName = info.getServiceName();
                Ln.i("mDNS service registered: " + registeredName);
                registered = true;
                if (callback != null) {
                    callback.onRegistered(registeredName);
                }
            }

            @Override
            public void onRegistrationFailed(NsdServiceInfo info, int errorCode) {
                Ln.e("mDNS registration failed: " + errorCode);
                registered = false;
                if (callback != null) {
                    callback.onRegistrationFailed(errorCode);
                }
            }

            @Override
            public void onServiceUnregistered(NsdServiceInfo info) {
                Ln.i("mDNS service unregistered: " + info.getServiceName());
                registered = false;
                if (callback != null) {
                    callback.onUnregistered();
                }
            }

            @Override
            public void onUnregistrationFailed(NsdServiceInfo info, int errorCode) {
                Ln.e("mDNS unregistration failed: " + errorCode);
                if (callback != null) {
                    callback.onUnregistrationFailed(errorCode);
                }
            }
        };

        try {
            nsdManager.registerService(serviceInfo, NsdManager.PROTOCOL_DNS_SD,
                                       registrationListener);
            Ln.i("Starting mDNS broadcast: " + serviceName + " on port " + port);
        } catch (Exception e) {
            Ln.e("Failed to start mDNS service", e);
            if (callback != null) {
                callback.onRegistrationFailed(-1);
            }
        }
    }

    /**
     * Stop broadcasting the AllRelay service.
     */
    public void stop() {
        if (!registered || registrationListener == null) {
            return;
        }

        try {
            nsdManager.unregisterService(registrationListener);
            Ln.i("Stopping mDNS broadcast: " + serviceName);
        } catch (Exception e) {
            Ln.w("Error stopping mDNS service", e);
        }

        registered = false;
    }

    /**
     * Check if the service is currently registered.
     */
    public boolean isRegistered() {
        return registered;
    }

    /**
     * Get the service name being broadcast.
     */
    public String getServiceName() {
        return serviceName;
    }

    /**
     * Sanitize device name for use as mDNS service name.
     * mDNS service names must be valid DNS-SD names.
     */
    private static String sanitizeServiceName(String name) {
        if (name == null || name.isEmpty()) {
            return "Unknown";
        }
        // Replace spaces and special characters with hyphens
        return name.replaceAll("[^a-zA-Z0-9\\-]", "-")
                   .replaceAll("-+", "-")
                   .replaceAll("^-|-$", "");
    }

    /**
     * Callback interface for mDNS registration events.
     */
    public interface RegistrationCallback {
        void onRegistered(String serviceName);
        void onRegistrationFailed(int errorCode);
        void onUnregistered();
        void onUnregistrationFailed(int errorCode);
    }
}
