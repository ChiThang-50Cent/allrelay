package com.genymobile.scrcpy.device;

import org.junit.Assert;
import org.junit.Test;

import java.util.Map;

public class FoldStateParserTest {

    private static final String SAMPLE_PRINT_STATES =
            "Supported states: [\n" +
            "  DeviceState{identifier=0, name='CLOSED', app_accessible=false, cancel_when_requester_not_on_top=false},\n" +
            "  DeviceState{identifier=1, name='TENT', app_accessible=false, cancel_when_requester_not_on_top=false},\n" +
            "  DeviceState{identifier=2, name='HALF_OPENED', app_accessible=true, cancel_when_requester_not_on_top=false},\n" +
            "  DeviceState{identifier=3, name='OPENED', app_accessible=true, cancel_when_requester_not_on_top=false},\n" +
            "  DeviceState{identifier=4, name='CONCURRENT_INNER_DEFAULT', app_accessible=true, cancel_when_requester_not_on_top=true},\n" +
            "]\n";

    private static final String SAMPLE_DUMPSYS_NO_OVERRIDE =
            "DEVICE STATE MANAGER (dumpsys device_state)\n" +
            "  mCommittedState=Optional[DeviceState{identifier=0, name='CLOSED', app_accessible=false, cancel_when_requester_not_on_top=false}]\n" +
            "  mPendingState=Optional.empty\n" +
            "  mBaseState=Optional[DeviceState{identifier=0, name='CLOSED', app_accessible=false, cancel_when_requester_not_on_top=false}]\n" +
            "  mOverrideState=Optional.empty\n" +
            "  mDeviceStates={0=DeviceState{identifier=0, name='CLOSED'}, 1=DeviceState{identifier=1, name='TENT'}, 2=DeviceState{identifier=2, name='HALF_OPENED'}, 3=DeviceState{identifier=3, name='OPENED'}, 4=DeviceState{identifier=4, name='CONCURRENT_INNER_DEFAULT'}}\n" +
            "Override Request active: false\n";

    private static final String SAMPLE_DUMPSYS_OVERRIDE_SAME_BASE =
            "DEVICE STATE MANAGER (dumpsys device_state)\n" +
            "  mCommittedState=Optional[DeviceState{identifier=0, name='CLOSED', app_accessible=false, cancel_when_requester_not_on_top=false}]\n" +
            "  mPendingState=Optional.empty\n" +
            "  mBaseState=Optional[DeviceState{identifier=0, name='CLOSED', app_accessible=false, cancel_when_requester_not_on_top=false}]\n" +
            "  mOverrideState=Optional[DeviceState{identifier=0, name='CLOSED', app_accessible=false, cancel_when_requester_not_on_top=false}]\n" +
            "Override Request active: true\n";

    private static final String SAMPLE_DUMPSYS_OVERRIDE_OPENED =
            "DEVICE STATE MANAGER (dumpsys device_state)\n" +
            "  mCommittedState=Optional[DeviceState{identifier=3, name='OPENED', app_accessible=true, cancel_when_requester_not_on_top=false}]\n" +
            "  mPendingState=Optional.empty\n" +
            "  mBaseState=Optional[DeviceState{identifier=0, name='CLOSED', app_accessible=false, cancel_when_requester_not_on_top=false}]\n" +
            "  mOverrideState=Optional[DeviceState{identifier=3, name='OPENED', app_accessible=true, cancel_when_requester_not_on_top=false}]\n" +
            "Override Request active: true\n";

    @Test
    public void testParseSupportedStatesFromPrintStates() {
        Map<Integer, String> states = FoldStateParser.parseSupportedStates(SAMPLE_PRINT_STATES);
        Assert.assertEquals(5, states.size());
        Assert.assertEquals("CLOSED", states.get(0));
        Assert.assertEquals("TENT", states.get(1));
        Assert.assertEquals("HALF_OPENED", states.get(2));
        Assert.assertEquals("OPENED", states.get(3));
        Assert.assertEquals("CONCURRENT_INNER_DEFAULT", states.get(4));
    }

    @Test
    public void testParseSupportedStatesFromDumpsys() {
        Map<Integer, String> states = FoldStateParser.parseSupportedStates(SAMPLE_DUMPSYS_NO_OVERRIDE);
        Assert.assertEquals(5, states.size());
        Assert.assertEquals("CLOSED", states.get(0));
        Assert.assertEquals("OPENED", states.get(3));
    }

    @Test
    public void testParseOverrideInactive() {
        Assert.assertFalse(FoldStateParser.parseOverrideActive(SAMPLE_DUMPSYS_NO_OVERRIDE));
        Assert.assertNull(FoldStateParser.parseOverrideStateId(SAMPLE_DUMPSYS_NO_OVERRIDE));
        Assert.assertEquals(Integer.valueOf(0), FoldStateParser.parseBaseStateId(SAMPLE_DUMPSYS_NO_OVERRIDE));
    }

    @Test
    public void testParseOverrideActiveWhenCurrentEqualsBase() {
        // Even when current == base == 0, external override is active
        Assert.assertTrue(FoldStateParser.parseOverrideActive(SAMPLE_DUMPSYS_OVERRIDE_SAME_BASE));
        Assert.assertEquals(Integer.valueOf(0), FoldStateParser.parseOverrideStateId(SAMPLE_DUMPSYS_OVERRIDE_SAME_BASE));
        Assert.assertEquals(Integer.valueOf(0), FoldStateParser.parseBaseStateId(SAMPLE_DUMPSYS_OVERRIDE_SAME_BASE));
    }

    @Test
    public void testParseOverrideActiveOpened() {
        Assert.assertTrue(FoldStateParser.parseOverrideActive(SAMPLE_DUMPSYS_OVERRIDE_OPENED));
        Assert.assertEquals(Integer.valueOf(3), FoldStateParser.parseOverrideStateId(SAMPLE_DUMPSYS_OVERRIDE_OPENED));
        Assert.assertEquals(Integer.valueOf(0), FoldStateParser.parseBaseStateId(SAMPLE_DUMPSYS_OVERRIDE_OPENED));
    }

    @Test
    public void testIsOpenedNameExactMatch() {
        Assert.assertTrue(FoldStateParser.isOpenedName("OPENED"));
        Assert.assertTrue(FoldStateParser.isOpenedName("opened"));
        Assert.assertTrue(FoldStateParser.isOpenedName("OPEN"));
        Assert.assertTrue(FoldStateParser.isOpenedName("open"));

        // Must reject half-opened or concurrent states
        Assert.assertFalse(FoldStateParser.isOpenedName("HALF_OPENED"));
        Assert.assertFalse(FoldStateParser.isOpenedName("half_opened"));
        Assert.assertFalse(FoldStateParser.isOpenedName("CONCURRENT_INNER_DEFAULT"));
        Assert.assertFalse(FoldStateParser.isOpenedName("CLOSED"));
        Assert.assertFalse(FoldStateParser.isOpenedName("TENT"));
        Assert.assertFalse(FoldStateParser.isOpenedName(null));
        Assert.assertFalse(FoldStateParser.isOpenedName(""));
    }

    @Test
    public void testIsClosedName() {
        Assert.assertTrue(FoldStateParser.isClosedName("CLOSED"));
        Assert.assertTrue(FoldStateParser.isClosedName("closed"));
        Assert.assertTrue(FoldStateParser.isClosedName("CLOSED_LID"));

        Assert.assertFalse(FoldStateParser.isClosedName("OPENED"));
        Assert.assertFalse(FoldStateParser.isClosedName("HALF_OPENED"));
        Assert.assertFalse(FoldStateParser.isClosedName("TENT"));
        Assert.assertFalse(FoldStateParser.isClosedName(null));
        Assert.assertFalse(FoldStateParser.isClosedName(""));
    }

    @Test
    public void testArbitraryStateIdentifiers() {
        String customDumpsys =
                "mBaseState=Optional[DeviceState{identifier=10, name='CLOSED'}]\n" +
                "mOverrideState=Optional[DeviceState{identifier=20, name='OPENED'}]\n" +
                "Override Request active: true\n";

        Assert.assertTrue(FoldStateParser.parseOverrideActive(customDumpsys));
        Assert.assertEquals(Integer.valueOf(20), FoldStateParser.parseOverrideStateId(customDumpsys));
        Assert.assertEquals(Integer.valueOf(10), FoldStateParser.parseBaseStateId(customDumpsys));
    }

    @Test
    public void testPhysicallyOpenedNoOverride() {
        String dumpsys =
                "DEVICE STATE MANAGER (dumpsys device_state)\n" +
                "  mCommittedState=Optional[DeviceState{identifier=3, name='OPENED', app_accessible=true, cancel_when_requester_not_on_top=false}]\n" +
                "  mBaseState=Optional[DeviceState{identifier=3, name='OPENED', app_accessible=true, cancel_when_requester_not_on_top=false}]\n" +
                "  mOverrideState=Optional.empty\n" +
                "Override Request active: false\n";

        Assert.assertFalse(FoldStateParser.parseOverrideActive(dumpsys));
        Assert.assertNull(FoldStateParser.parseOverrideStateId(dumpsys));
        Assert.assertEquals(Integer.valueOf(3), FoldStateParser.parseBaseStateId(dumpsys));
        Map<Integer, String> states = FoldStateParser.parseSupportedStates(SAMPLE_PRINT_STATES);
        String baseName = states.get(FoldStateParser.parseBaseStateId(dumpsys));
        Assert.assertEquals("OPENED", baseName);
        Assert.assertTrue(FoldStateParser.isOpenedName(baseName));
        Assert.assertFalse(FoldStateParser.isClosedName(baseName));
    }

    @Test
    public void testConflictingExternalOverrideDetection() {
        // Device is physically closed (base=0), but external override pinned to TENT (1)
        String dumpsys =
                "DEVICE STATE MANAGER (dumpsys device_state)\n" +
                "  mCommittedState=Optional[DeviceState{identifier=1, name='TENT'}]\n" +
                "  mBaseState=Optional[DeviceState{identifier=0, name='CLOSED'}]\n" +
                "  mOverrideState=Optional[DeviceState{identifier=1, name='TENT'}]\n" +
                "Override Request active: true\n";

        Assert.assertTrue(FoldStateParser.parseOverrideActive(dumpsys));
        Integer overrideId = FoldStateParser.parseOverrideStateId(dumpsys);
        Assert.assertEquals(Integer.valueOf(1), overrideId);

        // Opened ID is 3; external override ID is 1 -> conflict
        int openedId = 3;
        Assert.assertNotEquals(Integer.valueOf(openedId), overrideId);
    }
}
