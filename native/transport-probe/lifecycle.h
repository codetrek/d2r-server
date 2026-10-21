#ifndef D2R_LIFECYCLE_H
#define D2R_LIFECYCLE_H

#include <stddef.h>
#include <stdint.h>

#define D2R_LIFE_MAGIC "D2RLC001"
#define D2R_LIFE_VERSION 1u
#define D2R_LIFE_HEADER_SIZE 256u
#define D2R_LIFE_RECORD_SIZE 160u
#define D2R_LIFE_CAPACITY (D2R_LIFE_HEADER_SIZE + D2R_LIFE_RECORD_SIZE * 16384u)
#define D2R_LIFE_GATE_UI 1u
#define D2R_LIFE_GATE_TICK 2u
#define D2R_LIFE_GATE_COUNT 23u

enum { D2R_LIFE_STARTED = 1u, D2R_LIFE_PROFILE_MATCHED = 2u,
       D2R_LIFE_CLOSED = 4u, D2R_LIFE_GAP = 8u };
enum { D2R_LIFE_UI_METADATA = 1u, D2R_LIFE_UI_UNREADABLE = 2u,
       D2R_LIFE_PARENT_PRESENT = 4u, D2R_LIFE_PARENT_STATE = 8u,
       D2R_LIFE_PARENT_UNREADABLE = 16u };

typedef struct {
    uint8_t magic[8];
    uint32_t version, header_size;
    uint64_t capacity, committed, records, callbacks, errors, overflow;
    uint64_t thread_violations, flags, owner_thread, record_size;
    uint64_t started_ns, closed_ns;
    uint8_t main_sha256[32], sdk_sha256[32], reserved[80];
} d2r_lifecycle_header;

typedef struct {
    uint64_t sequence, frame, monotonic_ns, transport_sequence;
    uint32_t gate, phase;
    uint64_t ui_category, ui_action;
    uint32_t flags, operation_mode;
    uint32_t state_b90, state_b94, state_b98, state_b9c;
    uint32_t clock_e7c, error_code, timer_1f0, timer_1f4;
    uint32_t deferred_messages, transport_mode;
    uint64_t last_receive, last_send;
    uint32_t parent_state_58, reserved0;
    uint64_t error_deadline;
    uint8_t state_ba1, frontend_error, transport_closed;
    uint8_t first_handle_present, second_handle_present, pending_present, reserved[18];
} d2r_lifecycle_record;

_Static_assert(sizeof(d2r_lifecycle_header) == D2R_LIFE_HEADER_SIZE, "lifecycle header ABI");
_Static_assert(offsetof(d2r_lifecycle_header, flags) == 72, "lifecycle flags ABI");
_Static_assert(offsetof(d2r_lifecycle_header, main_sha256) == 112, "lifecycle identity ABI");
_Static_assert(sizeof(d2r_lifecycle_record) == D2R_LIFE_RECORD_SIZE, "lifecycle record ABI");
_Static_assert(offsetof(d2r_lifecycle_record, flags) == 56, "lifecycle record flags ABI");
_Static_assert(offsetof(d2r_lifecycle_record, error_deadline) == 128, "lifecycle deadline ABI");

int d2r_lifecycle_start(uint8_t *base, size_t size, const char *path,
                       uint64_t (*frame)(void), uint64_t (*sequence)(void));
void d2r_lifecycle_profile_matched(void);
unsigned d2r_lifecycle_enter(unsigned gate, const uint64_t *registers);
void d2r_lifecycle_leave(unsigned token, unsigned gate, const uint64_t *registers);
int d2r_lifecycle_close(void);
int d2r_lifecycle_callback_active(void);
void gate_obs_set(int id, void (*callback)(const uint64_t *));
void d2r_upstream_gate_obs_set(int id, void (*callback)(const uint64_t *));

#ifdef D2R_PROBE_TESTING
void d2r_lifecycle_test_reset(void);
unsigned d2r_lifecycle_test_phase(void);
void d2r_lifecycle_test_break_descriptor(void);
#endif

#endif
