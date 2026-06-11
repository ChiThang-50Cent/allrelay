package com.genymobile.scrcpy.video;

import com.genymobile.scrcpy.AsyncProcessor;
import com.genymobile.scrcpy.Options;
import com.genymobile.scrcpy.device.WifiStreamer;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * Orchestrates simultaneous screen and camera capture over Wi-Fi.
 *
 * Each capture runs on its own encoder + streamer pair, writing to
 * separate TCP ports (5000 for screen, 5001 for camera).
 *
 * Both run as independent {@link AsyncProcessor}s on separate threads,
 * allowing the client to toggle them independently.
 *
 * <p>This is the multi-stream coordinator — the key Phase 2 class.</p>
 */
public final class MultiCapture {

    private final SurfaceCapture screenCapture;
    private final SurfaceCapture cameraCapture;
    private final WifiStreamer screenStreamer;
    private final WifiStreamer cameraStreamer;
    private final Options options;

    /**
     * Create a multi-capture coordinator.
     *
     * @param screenCapture  the screen capture source (may be null if screen is disabled)
     * @param cameraCapture  the camera capture source (may be null if camera is disabled)
     * @param screenStreamer the streamer for screen video output
     * @param cameraStreamer the streamer for camera video output
     * @param options        the AllRelay options
     */
    public MultiCapture(SurfaceCapture screenCapture,
                        SurfaceCapture cameraCapture,
                        WifiStreamer screenStreamer,
                        WifiStreamer cameraStreamer,
                        Options options) {
        this.screenCapture = screenCapture;
        this.cameraCapture = cameraCapture;
        this.screenStreamer = screenStreamer;
        this.cameraStreamer = cameraStreamer;
        this.options = options;
    }

    /**
     * Create a list of AsyncProcessors, one per active stream.
     *
     * <p>Each processor is a {@link WifiSurfaceEncoder} that runs the
     * full capture → encode → stream pipeline on its own thread.</p>
     *
     * @return immutable list of async processors (may be empty if no streams)
     */
    public List<AsyncProcessor> createProcessors() {
        List<AsyncProcessor> processors = new ArrayList<>(2);

        if (screenCapture != null && screenStreamer != null) {
            processors.add(new WifiSurfaceEncoder(screenCapture, screenStreamer, options));
        }

        if (cameraCapture != null && cameraStreamer != null) {
            processors.add(new WifiSurfaceEncoder(cameraCapture, cameraStreamer, options));
        }

        return Collections.unmodifiableList(processors);
    }

    /**
     * Get the screen capture for input/controller binding.
     */
    public SurfaceCapture getScreenCapture() {
        return screenCapture;
    }

    /**
     * Get the camera capture for torch/zoom commands.
     */
    public SurfaceCapture getCameraCapture() {
        return cameraCapture;
    }
}
