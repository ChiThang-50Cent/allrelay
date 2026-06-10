#include "demuxer.h"

#include <assert.h>
#include <inttypes.h>
#include <libavcodec/avcodec.h>
#include <libavutil/channel_layout.h>

#include "packet_merger.h"
#include "util/binary.h"
#include "util/log.h"

// AllRelay stream IDs
#define SC_STREAM_ID_SCREEN  UINT32_C(0x00000001)
#define SC_STREAM_ID_CAMERA  UINT32_C(0x00000002)
#define SC_STREAM_ID_MIC     UINT32_C(0x00000003)
#define SC_STREAM_ID_SPEAKER UINT32_C(0x00000004)

// Header: [stream_id (4)] [pts+flags (8)] [packet_size (4)] = 16 bytes
#define SC_PACKET_HEADER_SIZE 16

#define SC_PACKET_FLAG_CONFIG    (UINT64_C(1) << 62)
#define SC_PACKET_FLAG_KEY_FRAME (UINT64_C(1) << 61)

#define SC_PACKET_PTS_MASK (SC_PACKET_FLAG_KEY_FRAME - 1)

static enum AVCodecID
sc_demuxer_to_avcodec_id(uint32_t codec_id) {
#define SC_CODEC_ID_H264 UINT32_C(0x68323634) // "h264" in ASCII
#define SC_CODEC_ID_H265 UINT32_C(0x68323635) // "h265" in ASCII
#define SC_CODEC_ID_AV1 UINT32_C(0x00617631) // "av1" in ASCII
#define SC_CODEC_ID_OPUS UINT32_C(0x6f707573) // "opus" in ASCII
#define SC_CODEC_ID_AAC UINT32_C(0x00616163) // "aac" in ASCII
#define SC_CODEC_ID_FLAC UINT32_C(0x666c6163) // "flac" in ASCII
#define SC_CODEC_ID_RAW UINT32_C(0x00726177) // "raw" in ASCII
    switch (codec_id) {
        case SC_CODEC_ID_H264:
            return AV_CODEC_ID_H264;
        case SC_CODEC_ID_H265:
            return AV_CODEC_ID_HEVC;
        case SC_CODEC_ID_AV1:
#ifdef SCRCPY_LAVC_HAS_AV1
            return AV_CODEC_ID_AV1;
#else
            LOGE("AV1 not supported by this FFmpeg version");
            return AV_CODEC_ID_NONE;
#endif
        case SC_CODEC_ID_OPUS:
            return AV_CODEC_ID_OPUS;
        case SC_CODEC_ID_AAC:
            return AV_CODEC_ID_AAC;
        case SC_CODEC_ID_FLAC:
            return AV_CODEC_ID_FLAC;
        case SC_CODEC_ID_RAW:
            return AV_CODEC_ID_PCM_S16LE;
        default:
            LOGE("Unknown codec id 0x%08" PRIx32, codec_id);
            return AV_CODEC_ID_NONE;
    }
}

static bool
sc_demuxer_recv_codec_id(struct sc_demuxer *demuxer, uint32_t *codec_id) {
    uint8_t data[4];
    ssize_t r = net_recv_all(demuxer->socket, data, 4);
    if (r < 4) {
        return false;
    }

    *codec_id = sc_read32be(data);
    return true;
}

static inline bool
sc_demuxer_recv_header(struct sc_demuxer *demuxer,
                       uint8_t buf[static SC_PACKET_HEADER_SIZE]) {
    // AllRelay packet header format (16 bytes):
    //
    //  byte 0   byte 1   byte 2   byte 3   byte 4-11                    byte 12-15
    // ........ ........ ........ ........ [pts+flags (8 bytes)]          [packet_size (4)]
    // <---------------------------------> <---------------------------> <------------->
    //          stream_id (4)                       pts+flags              packet size
    //
    // Stream IDs:
    //   0x00000001 = SCREEN
    //   0x00000002 = CAMERA
    //   0x00000003 = MIC
    //   0x00000004 = SPEAKER
    //
    // If the MSB of pts+flags is 1, then it is a session packet (video only):
    //  byte 4   byte 5   byte 6   byte 7
    // 10000000 00000000 00000000 0000000.
    // ^<------------------------------->^
    // |               padding           |
    //  `- session packet flag            `- client resized flag
    //
    //  byte 8-11: video width
    //  byte 12-15: video height
    //
    // Otherwise (media packet):
    //  byte 4-11: 0CK..... PTS (C=config, K=keyframe)
    //  byte 12-15: packet size
    //
    ssize_t r = net_recv_all(demuxer->socket, buf, SC_PACKET_HEADER_SIZE);
    assert(r <= SC_PACKET_HEADER_SIZE);
    return r == SC_PACKET_HEADER_SIZE;
}

static bool
sc_demuxer_is_session(const uint8_t *header) {
    // Session flag is in the pts+flags field (bytes 4-11), check MSB of byte 4
    return header[4] & 0x80;
}

static uint32_t
sc_demuxer_get_stream_id(const uint8_t *header) {
    return sc_read32be(header);
}

static void
sc_demuxer_parse_session(const uint8_t *header,
                         struct sc_stream_session *session) {
    assert(sc_demuxer_is_session(header));
    session->video.width = sc_read32be(&header[8]);
    session->video.height = sc_read32be(&header[12]);
    session->video.client_resized = header[7] & 1;
}

static bool
sc_demuxer_recv_packet(struct sc_demuxer *demuxer, const uint8_t *header,
                       AVPacket *packet) {
    assert(!sc_demuxer_is_session(header));
    uint32_t stream_id = sc_read32be(header); // bytes 0-3
    uint64_t pts_flags = sc_read64be(&header[4]); // bytes 4-11
    uint32_t len = sc_read32be(&header[12]); // bytes 12-15

    // Store stream_id in demuxer for downstream consumers
    demuxer->stream_id = stream_id;

    LOGD("Demuxer '%s': stream_id=0x%08" PRIx32, demuxer->name, stream_id);

    if (!len) {
        LOGE("Invalid packet length: 0");
        return false;
    }

    if (av_new_packet(packet, len)) {
        LOG_OOM();
        return false;
    }

    ssize_t r = net_recv_all(demuxer->socket, packet->data, len);
    if (r < 0 || ((uint32_t) r) < len) {
        av_packet_unref(packet);
        return false;
    }

    if (pts_flags & SC_PACKET_FLAG_CONFIG) {
        packet->pts = AV_NOPTS_VALUE;
    } else {
        packet->pts = pts_flags & SC_PACKET_PTS_MASK;
    }

    if (pts_flags & SC_PACKET_FLAG_KEY_FRAME) {
        packet->flags |= AV_PKT_FLAG_KEY;
    }

    packet->dts = packet->pts;
    return true;
}

static int
run_demuxer(void *data) {
    struct sc_demuxer *demuxer = data;

    // Flag to report end-of-stream (i.e. device disconnected)
    enum sc_demuxer_status status = SC_DEMUXER_STATUS_ERROR;

    uint32_t raw_codec_id;
    bool ok = sc_demuxer_recv_codec_id(demuxer, &raw_codec_id);
    if (!ok) {
        LOGE("Demuxer '%s': stream disabled due to connection error",
             demuxer->name);
        goto end;
    }

    if (raw_codec_id == 0) {
        LOGW("Demuxer '%s': stream explicitly disabled by the device",
             demuxer->name);
        sc_packet_source_sinks_disable(&demuxer->packet_source);
        status = SC_DEMUXER_STATUS_DISABLED;
        goto end;
    }

    if (raw_codec_id == 1) {
        LOGE("Demuxer '%s': stream configuration error on the device",
             demuxer->name);
        goto end;
    }

    enum AVCodecID codec_id = sc_demuxer_to_avcodec_id(raw_codec_id);
    if (codec_id == AV_CODEC_ID_NONE) {
        LOGE("Demuxer '%s': stream disabled due to unsupported codec",
             demuxer->name);
        sc_packet_source_sinks_disable(&demuxer->packet_source);
        goto end;
    }

    const AVCodec *codec = avcodec_find_decoder(codec_id);
    if (!codec) {
        LOGE("Demuxer '%s': stream disabled due to missing decoder",
             demuxer->name);
        sc_packet_source_sinks_disable(&demuxer->packet_source);
        goto end;
    }

    AVCodecContext *codec_ctx = avcodec_alloc_context3(codec);
    if (!codec_ctx) {
        LOG_OOM();
        goto end;
    }

    codec_ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;

    uint8_t header[SC_PACKET_HEADER_SIZE];
    struct sc_stream_session session_data;

    struct sc_stream_session *session = NULL;
    if (codec->type == AVMEDIA_TYPE_VIDEO) {
        bool ok = sc_demuxer_recv_header(demuxer, header);
        if (!ok) {
            goto finally_free_context;
        }

        if (!sc_demuxer_is_session(header)) {
            LOGE("Unexpected packet (not a session header)");
            goto finally_free_context;
        }

        session = &session_data;
        sc_demuxer_parse_session(header, session);

        codec_ctx->width = session_data.video.width;
        codec_ctx->height = session_data.video.height;
        codec_ctx->pix_fmt = AV_PIX_FMT_YUV420P;

    } else {
        // Hardcoded audio properties
#ifdef SCRCPY_LAVU_HAS_CHLAYOUT
        codec_ctx->ch_layout = (AVChannelLayout) AV_CHANNEL_LAYOUT_STEREO;
#else
        codec_ctx->channel_layout = AV_CH_LAYOUT_STEREO;
        codec_ctx->channels = 2;
#endif
        codec_ctx->sample_rate = 48000;

        if (raw_codec_id == SC_CODEC_ID_FLAC) {
            // The sample_fmt is not set by the FLAC decoder
            codec_ctx->sample_fmt = AV_SAMPLE_FMT_S16;
        }
    }

    if (avcodec_open2(codec_ctx, codec, NULL) < 0) {
        LOGE("Demuxer '%s': could not open codec", demuxer->name);
        goto finally_free_context;
    }

    if (!sc_packet_source_sinks_open(&demuxer->packet_source, codec_ctx,
                                     session)) {
        goto finally_free_context;
    }

    // Config packets must be merged with the next non-config packet only for
    // H.26x
    bool must_merge_config_packet = raw_codec_id == SC_CODEC_ID_H264
                                 || raw_codec_id == SC_CODEC_ID_H265;

    struct sc_packet_merger merger;

    if (must_merge_config_packet) {
        sc_packet_merger_init(&merger);
    }

    AVPacket *packet = av_packet_alloc();
    if (!packet) {
        LOG_OOM();
        goto finally_close_sinks;
    }

    for (;;) {
        bool ok = sc_demuxer_recv_header(demuxer, header);
        if (!ok) {
            // end of stream
            status = SC_DEMUXER_STATUS_EOS;
            break;
        }

        if (sc_demuxer_is_session(header)) {
            sc_demuxer_parse_session(header, &session_data);
            ok = sc_packet_source_sinks_push_session(&demuxer->packet_source,
                                                     &session_data);
            if (!ok) {
                // The sink already logged its concrete error
                break;
            }
        } else {
            bool ok = sc_demuxer_recv_packet(demuxer, header, packet);
            if (!ok) {
                break;
            }

            if (must_merge_config_packet) {
                // Prepend any config packet to the next media packet
                ok = sc_packet_merger_merge(&merger, packet);
                if (!ok) {
                    av_packet_unref(packet);
                    break;
                }
            }

            ok = sc_packet_source_sinks_push(&demuxer->packet_source, packet);
            av_packet_unref(packet);
            if (!ok) {
                // The sink already logged its concrete error
                break;
            }
        }
    }

    LOGD("Demuxer '%s': end of frames", demuxer->name);

    if (must_merge_config_packet) {
        sc_packet_merger_destroy(&merger);
    }

    av_packet_free(&packet);
finally_close_sinks:
    sc_packet_source_sinks_close(&demuxer->packet_source);
finally_free_context:
    avcodec_free_context(&codec_ctx);
end:
    demuxer->cbs->on_ended(demuxer, status, demuxer->cbs_userdata);

    return 0;
}

void
sc_demuxer_init(struct sc_demuxer *demuxer, const char *name, sc_socket socket,
                const struct sc_demuxer_callbacks *cbs, void *cbs_userdata) {
    assert(socket != SC_SOCKET_NONE);

    demuxer->name = name; // statically allocated
    demuxer->socket = socket;
    demuxer->stream_id = 0; // will be set from first packet
    sc_packet_source_init(&demuxer->packet_source);

    assert(cbs && cbs->on_ended);

    demuxer->cbs = cbs;
    demuxer->cbs_userdata = cbs_userdata;
}

bool
sc_demuxer_start(struct sc_demuxer *demuxer) {
    LOGD("Demuxer '%s': starting thread", demuxer->name);

    bool ok = sc_thread_create(&demuxer->thread, run_demuxer, "scrcpy-demuxer",
                               demuxer);
    if (!ok) {
        LOGE("Demuxer '%s': could not start thread", demuxer->name);
        return false;
    }
    return true;
}

void
sc_demuxer_join(struct sc_demuxer *demuxer) {
    sc_thread_join(&demuxer->thread, NULL);
}
