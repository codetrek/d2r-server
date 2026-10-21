#ifndef D2R_OWNED_PIPE_RELAY_H
#define D2R_OWNED_PIPE_RELAY_H
#include "probe.h"

#define D2R_PIPE_MAGIC "D2RRP001"
#define D2R_PIPE_VERSION 1u
#define D2R_PIPE_HEADER_SIZE 32u
#define D2R_PIPE_MAX_PAYLOAD (1024u * 1024u)
#define D2R_PIPE_MAX_FRAMES 65536u
#define D2R_PIPE_MAX_BYTES (64u * 1024u * 1024u)
#define D2R_PIPE_TIMEOUT_NS UINT64_C(2000000000)
enum { D2R_PIPE_C2S = 1, D2R_PIPE_S2C = 2, D2R_PIPE_CLOSE = 3, D2R_PIPE_ACK = 4 };

typedef struct {
    uint8_t magic[8];
    uint32_t version;
    uint16_t type, flags;
    uint64_t sequence;
    uint32_t client_id, payload_length;
} d2r_pipe_header;
_Static_assert(sizeof(d2r_pipe_header) == D2R_PIPE_HEADER_SIZE, "owned pipe header ABI");
_Static_assert(offsetof(d2r_pipe_header, sequence) == 16, "owned pipe sequence ABI");

int d2r_pipe_configure(const char *mode, const char *read_fd, const char *write_fd);
int d2r_pipe_prepare(d2r_probe_abi *state, const d2r_trace_record *record);
void d2r_pipe_release(d2r_probe_abi *state);
int d2r_pipe_close(void);
int d2r_pipe_active(void);
#ifdef D2R_PROBE_TESTING
void d2r_pipe_test_reset(void);
#endif
#endif
