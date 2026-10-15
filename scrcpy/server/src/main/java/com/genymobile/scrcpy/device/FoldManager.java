package com.genymobile.scrcpy.device;

import com.genymobile.scrcpy.FakeContext;
import com.genymobile.scrcpy.util.Ln;
import com.genymobile.scrcpy.wrappers.ServiceManager;

import android.os.IInterface;
import android.view.KeyEvent;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.lang.reflect.Array;
import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.TimeUnit;

public final class FoldManager {

    private static final int PROPERTY_FOLD_IN_CLOSED = 1;
    private static final int PROPERTY_FOLD_IN_OPEN = 3;

    private static final FoldManager INSTANCE = new FoldManager();

    private boolean overrideActiveByUs;
    private Map<Integer, String> cachedStateNames;

    public interface FoldSession extends AutoCloseable {
        @Override
        void close();
    }

    private FoldManager() {
    }

    public static FoldManager getInstance() {
        return INSTANCE;
    }

    private static IInterface getDeviceStateManagerService() {
        return ServiceManager.getDeviceStateManager();
    }

    private static Object getDeviceStateInfo(IInterface service) {
        if (service == null) {
            return null;
        }
        try {
            Method getDeviceStateInfoMethod = service.getClass().getMethod("getDeviceStateInfo");
            return getDeviceStateInfoMethod.invoke(service);
        } catch (Throwable t) {
            Ln.d("Could not invoke getDeviceStateInfo: " + t.getMessage());
            return null;
        }
    }

    private static Object getField(Object target, String name) {
        if (target == null) {
            return null;
        }
        try {
            Field f = target.getClass().getField(name);
            return f.get(target);
        } catch (Throwable t) {
            return null;
        }
    }

    private static int getStateIdentifier(Object stateObj) {
        if (stateObj == null) {
            return -1;
        }
        if (stateObj instanceof Integer) {
            return (Integer) stateObj;
        }
        try {
            Method m = stateObj.getClass().getMethod("getIdentifier");
            return (int) m.invoke(stateObj);
        } catch (Throwable t) {
            return -1;
        }
    }

    private static String getStateName(Object stateObj) {
        if (stateObj == null || stateObj instanceof Integer) {
            return "";
        }
        try {
            Method m = stateObj.getClass().getMethod("getName");
            Object name = m.invoke(stateObj);
            return name != null ? name.toString() : "";
        } catch (Throwable t) {
            return "";
        }
    }

    private static boolean hasStateProperty(Object stateObj, int property) {
        if (stateObj == null || stateObj instanceof Integer) {
            return false;
        }
        try {
            Method m = stateObj.getClass().getMethod("hasProperty", int.class);
            return (boolean) m.invoke(stateObj, property);
        } catch (Throwable t) {
            return false;
        }
    }

    private static String runCommandWithTimeout(String[] cmd, long timeoutMs) {
        Process process = null;
        try {
            process = Runtime.getRuntime().exec(cmd);
            boolean finished = process.waitFor(timeoutMs, TimeUnit.MILLISECONDS);
            if (!finished) {
                process.destroyForcibly();
                return "";
            }
            StringBuilder sb = new StringBuilder();
            try (BufferedReader reader = new BufferedReader(new InputStreamReader(process.getInputStream(), StandardCharsets.UTF_8))) {
                char[] buf = new char[1024];
                int n;
                while ((n = reader.read(buf)) != -1) {
                    sb.append(buf, 0, n);
                }
            }
            return sb.toString();
        } catch (InterruptedException ie) {
            Thread.currentThread().interrupt();
            if (process != null) {
                process.destroyForcibly();
            }
            return "";
        } catch (Throwable t) {
            if (process != null) {
                process.destroyForcibly();
            }
            return "";
        }
    }

    private synchronized Map<Integer, String> getOrResolveStateNames() {
        if (cachedStateNames != null && !cachedStateNames.isEmpty()) {
            return cachedStateNames;
        }

        // Try cmd device_state print-states first (bounded to 500ms)
        String printStatesText = runCommandWithTimeout(new String[]{"cmd", "device_state", "print-states"}, 500);
        Map<Integer, String> map = FoldStateParser.parseSupportedStates(printStatesText);

        // Try dumpsys device_state as fallback if map is still empty (bounded to 500ms)
        if (map.isEmpty()) {
            String dumpsysText = runCommandWithTimeout(new String[]{"dumpsys", "device_state"}, 500);
            map = FoldStateParser.parseSupportedStates(dumpsysText);
        }

        if (!map.isEmpty()) {
            cachedStateNames = map;
        }
        return map;
    }

    private boolean isStateClosed(Object stateObj) {
        if (stateObj == null) {
            return false;
        }
        if (hasStateProperty(stateObj, PROPERTY_FOLD_IN_CLOSED)) {
            return true;
        }
        String name = getStateName(stateObj);
        int id = getStateIdentifier(stateObj);
        if (name.isEmpty() && id >= 0) {
            Map<Integer, String> names = getOrResolveStateNames();
            name = names.getOrDefault(id, "");
        }
        return FoldStateParser.isClosedName(name);
    }

    private boolean isStateOpen(Object stateObj) {
        if (stateObj == null) {
            return false;
        }
        if (hasStateProperty(stateObj, PROPERTY_FOLD_IN_OPEN)) {
            return true;
        }
        String name = getStateName(stateObj);
        int id = getStateIdentifier(stateObj);
        if (name.isEmpty() && id >= 0) {
            Map<Integer, String> names = getOrResolveStateNames();
            name = names.getOrDefault(id, "");
        }
        return FoldStateParser.isOpenedName(name);
    }

    private int findOpenedStateId(Object supportedStatesObj) {
        if (supportedStatesObj == null) {
            Map<Integer, String> names = getOrResolveStateNames();
            for (Map.Entry<Integer, String> entry : names.entrySet()) {
                if (FoldStateParser.isOpenedName(entry.getValue())) {
                    return entry.getKey();
                }
            }
            return -1;
        }

        if (supportedStatesObj instanceof List<?>) {
            List<?> list = (List<?>) supportedStatesObj;
            for (Object item : list) {
                if (isStateOpen(item)) {
                    return getStateIdentifier(item);
                }
            }
        } else if (supportedStatesObj instanceof Object[]) {
            Object[] array = (Object[]) supportedStatesObj;
            for (Object item : array) {
                if (isStateOpen(item)) {
                    return getStateIdentifier(item);
                }
            }
        } else if (supportedStatesObj.getClass().isArray()) {
            int length = Array.getLength(supportedStatesObj);
            Map<Integer, String> names = getOrResolveStateNames();
            for (int i = 0; i < length; i++) {
                Object item = Array.get(supportedStatesObj, i);
                int id = getStateIdentifier(item);
                String name = names.getOrDefault(id, getStateName(item));
                if (FoldStateParser.isOpenedName(name)) {
                    return id;
                }
            }
        }

        Map<Integer, String> names = getOrResolveStateNames();
        for (Map.Entry<Integer, String> entry : names.entrySet()) {
            if (FoldStateParser.isOpenedName(entry.getValue())) {
                return entry.getKey();
            }
        }
        return -1;
    }

    public synchronized boolean isPhysicallyFolded() {
        IInterface service = getDeviceStateManagerService();
        if (service != null) {
            Object info = getDeviceStateInfo(service);
            if (info != null) {
                Object baseStateObj = getField(info, "baseState");
                if (baseStateObj != null) {
                    return isStateClosed(baseStateObj);
                }
            }
        }

        // Fallback via dumpsys device_state (bounded to 500ms)
        String dumpsysText = runCommandWithTimeout(new String[]{"dumpsys", "device_state"}, 500);
        Integer fallbackBaseId = FoldStateParser.parseBaseStateId(dumpsysText);
        if (fallbackBaseId != null) {
            Map<Integer, String> names = getOrResolveStateNames();
            String baseName = names.getOrDefault(fallbackBaseId, "");
            return FoldStateParser.isClosedName(baseName);
        }

        // If inspection failed but device is known to have a CLOSED state, do not assume unfolded!
        Map<Integer, String> knownNames = getOrResolveStateNames();
        for (String name : knownNames.values()) {
            if (FoldStateParser.isClosedName(name)) {
                Ln.w("Could not read current physical base state, but device has foldable CLOSED state; assuming folded for safety");
                return true;
            }
        }

        return false;
    }

    public synchronized boolean isOverrideActiveByUs() {
        return overrideActiveByUs;
    }

    public synchronized FoldSession prepareScreenSession() throws IOException {
        boolean foldOverrideNeeded = isPhysicallyFolded();
        boolean foldOverrideAcquired = false;

        if (foldOverrideNeeded) {
            IInterface service = getDeviceStateManagerService();
            if (service == null) {
                Ln.w("device_state service not found; skipping fold override");
            } else {
                Object info = getDeviceStateInfo(service);
                Object baseStateObj = info != null ? getField(info, "baseState") : null;
                Object currentStateObj = info != null ? getField(info, "currentState") : null;
                Object supportedStatesObj = info != null ? getField(info, "supportedStates") : null;

                int baseId = getStateIdentifier(baseStateObj);
                int currentId = getStateIdentifier(currentStateObj);
                int openedId = findOpenedStateId(supportedStatesObj);

                if (openedId == -1) {
                    throw new IOException("Device is physically folded, but no supported OPENED state was found");
                }

                // Check external override via dumpsys (detects active request even if current == base)
                String dumpsysText = runCommandWithTimeout(new String[]{"dumpsys", "device_state"}, 500);
                boolean externalOverrideActive = FoldStateParser.parseOverrideActive(dumpsysText) && !overrideActiveByUs;
                Integer externalOverrideId = FoldStateParser.parseOverrideStateId(dumpsysText);

                if (externalOverrideActive) {
                    if (externalOverrideId != null && externalOverrideId == openedId) {
                        Ln.i("Pre-existing external override to OPENED (" + openedId + ") is active; borrowing without owning/resetting");
                    } else {
                        throw new IOException("Cannot override fold state: external override already active (override=" +
                                externalOverrideId + ", base=" + baseId + ")");
                    }
                } else {
                    Ln.i("Requesting fold override to OPENED (id=" + openedId + ", base=" + baseId + ")");
                    requestState(openedId);
                    foldOverrideAcquired = true;

                    // Verify commit bounded to 1000ms
                    boolean committed = false;
                    long deadline = System.currentTimeMillis() + 1000;
                    int verifiedCurrentId = currentId;

                    while (System.currentTimeMillis() < deadline) {
                        try {
                            Thread.sleep(50);
                        } catch (InterruptedException e) {
                            Thread.currentThread().interrupt();
                            break;
                        }
                        Object checkInfo = getDeviceStateInfo(service);
                        if (checkInfo != null) {
                            Object checkCurrentObj = getField(checkInfo, "currentState");
                            verifiedCurrentId = getStateIdentifier(checkCurrentObj);
                            if (verifiedCurrentId == openedId) {
                                committed = true;
                                break;
                            }
                        }
                    }

                    if (!committed) {
                        try {
                            cancelStateRequest();
                        } catch (Exception e) {
                            Ln.w("Error cancelling uncommitted fold request", e);
                        } finally {
                            foldOverrideAcquired = false;
                        }
                        throw new IOException("Timed out waiting for fold override to OPENED (id=" + openedId +
                                ") to commit; current state is " + verifiedCurrentId);
                    }

                    overrideActiveByUs = true;
                    Ln.i("Fold override to OPENED (id=" + openedId + ") successfully active");
                }
            }
        } else {
            Ln.i("Device is not physically folded; proceeding without fold override");
        }

        // Now, for BOTH folded and unfolded/ordinary devices:
        // Ensure physical display is OFF and logically awake for video capture!
        try {
            ensureDisplayOffAndAwake();
        } catch (Throwable t) {
            if (foldOverrideAcquired) {
                try {
                    cancelStateRequest();
                } catch (Exception e) {
                    Ln.w("Error cancelling fold request after display init error", e);
                } finally {
                    overrideActiveByUs = false;
                }
            }
            if (t instanceof IOException) {
                throw (IOException) t;
            }
            throw new IOException("Failed during screen display preparation: " + t.getMessage(), t);
        }

        if (foldOverrideAcquired) {
            return this::releaseFoldOverride;
        } else {
            return () -> {}; // No-op session when no override was acquired
        }
    }

    public synchronized FoldSession prepareFoldedScreen() throws IOException {
        return prepareScreenSession();
    }

    private void ensureDisplayOffAndAwake() throws IOException {
        // Immediately set physical display panels to POWER_MODE_OFF so inner screen never lights up
        if (!Device.setDisplayPower(0, false)) {
            throw new IOException("Failed to set physical display power mode to OFF");
        }

        // If the logical display is sleeping/not interactive, wake it so SurfaceFlinger renders frames
        if (!Device.isScreenOn(0)) {
            Ln.i("Device is logically sleeping; waking up logical display while physical panel remains OFF");
            Device.pressReleaseKeycode(KeyEvent.KEYCODE_WAKEUP, 0, Device.INJECT_MODE_ASYNC);
            try {
                // Allow PowerManagerService asynchronous wake transition to settle
                Thread.sleep(300);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
            // Re-assert display power OFF to ensure the asynchronous wake does not cancel POWER_MODE_OFF
            if (!Device.setDisplayPower(0, false)) {
                throw new IOException("Failed to re-assert physical display power mode to OFF after wake");
            }
            Device.keepActive(0);
        }
    }

    public synchronized void releaseFoldOverride() {
        if (!overrideActiveByUs) {
            return;
        }
        Ln.i("Releasing Screen fold override");
        try {
            cancelStateRequest();
        } catch (Exception e) {
            Ln.w("Error cancelling fold state request: " + e.getMessage(), e);
        } finally {
            overrideActiveByUs = false;
        }
    }

    private void requestState(int stateId) throws IOException {
        try {
            Object dsm = getDeviceStateManager();
            if (dsm == null) {
                throw new IOException("DeviceStateManager system service not available");
            }
            Class<?> requestClass = Class.forName("android.hardware.devicestate.DeviceStateRequest");
            Method newBuilderMethod = requestClass.getMethod("newBuilder", int.class);
            Object builder = newBuilderMethod.invoke(null, stateId);
            Method buildMethod = builder.getClass().getMethod("build");
            Object request = buildMethod.invoke(builder);

            Method requestStateMethod = dsm.getClass().getMethod("requestState",
                    requestClass,
                    java.util.concurrent.Executor.class,
                    Class.forName("android.hardware.devicestate.DeviceStateRequest$Callback"));
            requestStateMethod.invoke(dsm, request, null, null);
        } catch (Exception e) {
            throw new IOException("Failed to request device state " + stateId + ": " + e.getMessage(), e);
        }
    }

    private void cancelStateRequest() throws Exception {
        Object dsm = getDeviceStateManager();
        if (dsm != null) {
            Method cancelMethod = dsm.getClass().getMethod("cancelStateRequest");
            cancelMethod.invoke(dsm);
        }
    }

    private Object getDeviceStateManager() {
        try {
            return FakeContext.get().getSystemService("device_state");
        } catch (Throwable t) {
            Ln.w("Could not get DeviceStateManager from FakeContext: " + t.getMessage());
            return null;
        }
    }
}
