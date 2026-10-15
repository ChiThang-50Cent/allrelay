package com.genymobile.scrcpy.device;

import java.util.HashMap;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public final class FoldStateParser {

    private static final Pattern PRINT_STATES_PATTERN = Pattern.compile("identifier=(\\d+)[^\\n]*?name='([^']+)'");
    private static final Pattern DUMPSYS_STATES_PATTERN = Pattern.compile("(\\d+)=DeviceState\\{identifier=\\d+, name='([^']+)'");
    private static final Pattern OVERRIDE_STATE_PATTERN = Pattern.compile("mOverrideState=Optional\\[DeviceState\\{identifier=(\\d+)");
    private static final Pattern BASE_STATE_OPT_PATTERN = Pattern.compile("mBaseState=Optional\\[DeviceState\\{identifier=(\\d+)");
    private static final Pattern BASE_STATE_INT_PATTERN = Pattern.compile("mBaseState=(\\d+)");

    private FoldStateParser() {
    }

    public static Map<Integer, String> parseSupportedStates(String text) {
        Map<Integer, String> map = new HashMap<>();
        if (text == null || text.isEmpty()) {
            return map;
        }

        Matcher m = PRINT_STATES_PATTERN.matcher(text);
        while (m.find()) {
            try {
                int id = Integer.parseInt(m.group(1));
                String name = m.group(2);
                map.put(id, name);
            } catch (NumberFormatException ignored) {
            }
        }

        if (map.isEmpty()) {
            m = DUMPSYS_STATES_PATTERN.matcher(text);
            while (m.find()) {
                try {
                    int id = Integer.parseInt(m.group(1));
                    String name = m.group(2);
                    map.put(id, name);
                } catch (NumberFormatException ignored) {
                }
            }
        }

        return map;
    }

    public static boolean parseOverrideActive(String dumpsysText) {
        if (dumpsysText == null || dumpsysText.isEmpty()) {
            return false;
        }
        if (dumpsysText.contains("Override Request active: true")) {
            return true;
        }
        Matcher m = OVERRIDE_STATE_PATTERN.matcher(dumpsysText);
        if (m.find()) {
            return true;
        }
        return false;
    }

    public static Integer parseOverrideStateId(String dumpsysText) {
        if (dumpsysText == null || dumpsysText.isEmpty()) {
            return null;
        }
        Matcher m = OVERRIDE_STATE_PATTERN.matcher(dumpsysText);
        if (m.find()) {
            try {
                return Integer.parseInt(m.group(1));
            } catch (NumberFormatException ignored) {
            }
        }
        return null;
    }

    public static Integer parseBaseStateId(String dumpsysText) {
        if (dumpsysText == null || dumpsysText.isEmpty()) {
            return null;
        }
        Matcher m = BASE_STATE_OPT_PATTERN.matcher(dumpsysText);
        if (m.find()) {
            try {
                return Integer.parseInt(m.group(1));
            } catch (NumberFormatException ignored) {
            }
        }
        m = BASE_STATE_INT_PATTERN.matcher(dumpsysText);
        if (m.find()) {
            try {
                return Integer.parseInt(m.group(1));
            } catch (NumberFormatException ignored) {
            }
        }
        return null;
    }

    public static boolean isClosedName(String name) {
        if (name == null || name.isEmpty()) {
            return false;
        }
        return name.equalsIgnoreCase("CLOSED") || name.toUpperCase(Locale.ENGLISH).startsWith("CLOSED");
    }

    public static boolean isOpenedName(String name) {
        if (name == null || name.isEmpty()) {
            return false;
        }
        if (name.equalsIgnoreCase("OPENED") || name.equalsIgnoreCase("OPEN")) {
            // Strictly exclude HALF_OPENED or CONCURRENT_INNER_DEFAULT
            return !name.equalsIgnoreCase("HALF_OPENED") && !name.toUpperCase(Locale.ENGLISH).contains("CONCURRENT");
        }
        return false;
    }
}
