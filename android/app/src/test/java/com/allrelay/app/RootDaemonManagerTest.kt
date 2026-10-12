package com.allrelay.app

import java.util.Base64
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class RootDaemonManagerTest {
    @Test
    fun normalizeAdbKeyLineRejectsMalformedInput() {
        val valid = adbKey(1)

        assertEquals(valid, RootDaemonManager.normalizeAdbKeyLine("  $valid  "))
        assertNull(RootDaemonManager.normalizeAdbKeyLine("$valid\n$valid"))
        assertNull(RootDaemonManager.normalizeAdbKeyLine("not-a-key"))
        assertNull(RootDaemonManager.normalizeAdbKeyLine("A".repeat(2_049)))
    }

    @Test
    fun mergeAdbKeyLinesDropsMalformedEntriesAndDeduplicatesByKeyMaterial() {
        val preserved = adbKey(1)
        val requested = adbKey(2)
        val duplicateWithOldComment = "$requested old-comment"
        val malformedOversizedLine = "A".repeat(3_000)

        val merged = RootDaemonManager.mergeAdbKeyLines(
            sequenceOf("$preserved first-host", duplicateWithOldComment, malformedOversizedLine),
            "$requested desktop-host",
        )

        assertEquals(
            listOf("$preserved first-host", "$requested desktop-host"),
            merged,
        )
    }

    private fun adbKey(marker: Int): String {
        return Base64.getEncoder().encodeToString(ByteArray(524).also {
            it[0] = 64
            it[8] = marker.toByte()
        })
    }
}
