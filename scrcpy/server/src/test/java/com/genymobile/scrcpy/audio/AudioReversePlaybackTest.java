package com.genymobile.scrcpy.audio;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;

public class AudioReversePlaybackTest {

    @Test
    public void testValidOpusHeadRequiresHeaderVersionAndChannels() {
        byte[] opusHead = {
                'O', 'p', 'u', 's', 'H', 'e', 'a', 'd',
                1, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0,
        };

        assertTrue(AudioReversePlayback.isValidOpusHead(opusHead));

        opusHead[0] = 'X';
        assertFalse(AudioReversePlayback.isValidOpusHead(opusHead));
        opusHead[0] = 'O';
        opusHead[8] = 0;
        assertFalse(AudioReversePlayback.isValidOpusHead(opusHead));
        opusHead[8] = 1;
        opusHead[9] = 0;
        assertFalse(AudioReversePlayback.isValidOpusHead(opusHead));
        assertFalse(AudioReversePlayback.isValidOpusHead(new byte[18]));
        assertFalse(AudioReversePlayback.isValidOpusHead(null));
    }

    @Test
    public void testNativeOrderLongCsdContainsExpectedValue() {
        long value = 6_500_000L;

        ByteBuffer csd = AudioReversePlayback.createNativeOrderLongCsd(value);

        assertEquals(0, csd.position());
        assertEquals(8, csd.remaining());
        assertEquals(value, csd.duplicate().order(ByteOrder.nativeOrder()).getLong());
    }
}
