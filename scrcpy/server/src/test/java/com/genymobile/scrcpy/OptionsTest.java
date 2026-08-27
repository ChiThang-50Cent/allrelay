package com.genymobile.scrcpy;


import org.junit.Assert;
import org.junit.Test;

public class OptionsTest {
    @Test
    public void testScreenVideoBitRateIsIndependentFromCameraVideoBitRate() {
        Options options = Options.parse(
                BuildConfig.VERSION_NAME,
                "video_bit_rate=3000000",
                "screen_video_bit_rate=16000000"
        );

        Assert.assertEquals(3000000, options.getVideoBitRate());
        Assert.assertEquals(16000000, options.getScreenVideoBitRate());
    }

}
