package com.genymobile.scrcpy.model;

/**
 * Identifies the type of stream in the AllRelay multi-stream protocol.
 *
 * The stream ID is written as the first 4 bytes of each packet header,
 * allowing the client to identify which stream a packet belongs to.
 *
 * Header format (16 bytes):
 *   [stream_id (4)] [pts+flags (8)] [packet_size (4)]
 */
public enum StreamId {
    SCREEN(0x00000001, "screen"),
    CAMERA(0x00000002, "camera"),
    MIC(0x00000003, "mic"),
    SPEAKER(0x00000004, "speaker");

    private final int id;
    private final String name;

    StreamId(int id, String name) {
        this.id = id;
        this.name = name;
    }

    public int getId() {
        return id;
    }

    public String getName() {
        return name;
    }

    public static StreamId fromId(int id) {
        for (StreamId streamId : values()) {
            if (streamId.id == id) {
                return streamId;
            }
        }
        return null;
    }
}
