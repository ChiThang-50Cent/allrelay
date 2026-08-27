package com.allrelay.app

import org.junit.Assert.assertEquals
import org.junit.Test

class AllRelayServiceTest {
    @Test
    fun adbLeaseRestoreActionMigratesLegacyActiveLease() {
        val now = 1_000L

        assertEquals(
            AllRelayService.Companion.AdbLeaseRestoreAction.RESTORE,
            AllRelayService.adbLeaseRestoreAction(now + 1, now, null),
        )
        assertEquals(
            AllRelayService.Companion.AdbLeaseRestoreAction.RESTORE,
            AllRelayService.adbLeaseRestoreAction(now + 1, now, true),
        )
        assertEquals(
            AllRelayService.Companion.AdbLeaseRestoreAction.CLEAR,
            AllRelayService.adbLeaseRestoreAction(now + 1, now, false),
        )
    }

    @Test
    fun adbLeaseRestoreActionDisablesExpiredLease() {
        val now = 1_000L

        assertEquals(
            AllRelayService.Companion.AdbLeaseRestoreAction.DISABLE,
            AllRelayService.adbLeaseRestoreAction(now, now, null),
        )
        assertEquals(
            AllRelayService.Companion.AdbLeaseRestoreAction.CLEAR,
            AllRelayService.adbLeaseRestoreAction(0, now, null),
        )
    }
}
