#ifndef SC_DEMUXER_H
#define SC_DEMUXER_H

#include "common.h"

#include <stdbool.h>
#include <stdint.h>
#include <inttypes.h>

#include "trait/packet_source.h"
#include "util/net.h"
#include "util/thread.h"

// AllRelay stream IDs (must match server-side StreamId.java)
#define SC_STREAM_ID_SCREEN  UINT32_C(0x00000001)
#define SC_STREAM_ID_CAMERA  UINT32_C(0x00000002)
#define SC_STREAM_ID_MIC     UINT32_C(0x00000003)
#define SC_STREAM_ID_SPEAKER UINT32_C(0x00000004)

struct sc_demuxer {
    struct sc_packet_source packet_source; // packet source trait

    const char *name; // must be statically allocated (e.g. a string literal)

    sc_socket socket;
    sc_thread thread;
    uint32_t stream_id; // AllRelay stream type (set after first packet)

    const struct sc_demuxer_callbacks *cbs;
    void *cbs_userdata;
};

enum sc_demuxer_status {
    SC_DEMUXER_STATUS_EOS,
    SC_DEMUXER_STATUS_DISABLED,
    SC_DEMUXER_STATUS_ERROR,
};

struct sc_demuxer_callbacks {
    void (*on_ended)(struct sc_demuxer *demuxer, enum sc_demuxer_status,
                     void *userdata);
};

// The name must be statically allocated (e.g. a string literal)
void
sc_demuxer_init(struct sc_demuxer *demuxer, const char *name, sc_socket socket,
                const struct sc_demuxer_callbacks *cbs, void *cbs_userdata);

bool
sc_demuxer_start(struct sc_demuxer *demuxer);

void
sc_demuxer_join(struct sc_demuxer *demuxer);

#endif
