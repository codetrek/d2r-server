#ifndef D2R_TRANSPORT_PROBE_FORMAT_H
#define D2R_TRANSPORT_PROBE_FORMAT_H

#include <stddef.h>
#include <stdint.h>

#define D2R_TRACE_MAGIC "D2RTP001"
#define D2R_TRACE_VERSION 1u
#define D2R_TRACE_HEADER_SIZE 256u
#define D2R_TRACE_RECORD_HEADER_SIZE 64u
#define D2R_TRACE_CAPACITY (64u * 1024u * 1024u)
#define D2R_TRACE_OBSERVATION_CAP (1024u * 1024u)

enum d2r_trace_header_flags {
    D2R_TRACE_INSTALLED = 1u,
    D2R_TRACE_PROFILE_MATCHED = 2u,
    D2R_TRACE_CLOSED = 4u,
    D2R_TRACE_CAPTURE_GAP = 8u
};

enum d2r_trace_kind {
    D2R_TRACE_SERVER_SEND_ATTEMPT = 1,
    D2R_TRACE_CLIENT_SEND_ATTEMPT = 2,
    D2R_TRACE_SYSTEM_POSTPOP = 3,
    D2R_TRACE_GAMEPLAY_POSTPOP = 4
};

enum d2r_trace_record_flags {
    D2R_RECORD_PAYLOAD = 1u,
    D2R_RECORD_IGNORED = 2u,
    D2R_RECORD_EMPTY = 4u,
    D2R_RECORD_BAD_SPAN = 8u,
    D2R_RECORD_CAP_EXCEEDED = 16u,
    D2R_RECORD_THREAD_VIOLATION = 32u,
    D2R_RECORD_CAPTURE_GAP = 64u,
    D2R_RECORD_CLIENT_ID = 128u
};

/* Darwin ARM64 is little endian. The reader treats these offsets as the file ABI;
 * the committed prefix is published only after the full aligned record is copied. */
typedef struct {
    uint8_t magic[8];
    uint32_t version;
    uint32_t header_size;
    uint64_t capacity;
    uint64_t committed;
    uint64_t records;
    uint64_t attempts;
    uint64_t pops;
    uint64_t empty_pops;
    uint64_t errors;
    uint64_t overflow;
    uint64_t thread_violations;
    uint64_t flags;
    uint64_t owner_thread;
    uint64_t observation_cap;
    uint8_t main_sha256[32];
    uint8_t sdk_sha256[32];
    uint8_t reserved[80];
} d2r_trace_header;

typedef struct {
    uint32_t record_size;
    uint16_t kind;
    uint16_t flags;
    uint64_t sequence;
    uint64_t frame;
    uint64_t thread;
    uint32_t client_id;
    uint32_t mode;
    uint32_t payload_length;
    uint32_t reserved0;
    uint64_t original_length;
    uint64_t reserved1;
} d2r_trace_record;

_Static_assert(sizeof(d2r_trace_header) == D2R_TRACE_HEADER_SIZE, "trace header ABI");
_Static_assert(sizeof(uintptr_t) == 8, "64-bit pointer ABI");
#if __BYTE_ORDER__ != __ORDER_LITTLE_ENDIAN__
#error This trace implementation requires little endian.
#endif
_Static_assert(offsetof(d2r_trace_header, committed) == 24, "committed ABI");
_Static_assert(offsetof(d2r_trace_header, flags) == 88, "flags ABI");
_Static_assert(sizeof(d2r_trace_record) == D2R_TRACE_RECORD_HEADER_SIZE, "record ABI");

#endif
