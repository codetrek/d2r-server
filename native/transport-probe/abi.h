#ifndef D2R_TRANSPORT_PROBE_ABI_H
#define D2R_TRANSPORT_PROBE_ABI_H

#define D2R_ABI_X0 0
#define D2R_ABI_X8 64
#define D2R_ABI_NZCV 72
#define D2R_ABI_Q0 80
#define D2R_ABI_ORIGINAL_X8 208
#define D2R_ABI_TARGET 216
#define D2R_ABI_KIND 224
#define D2R_ABI_STATE_SIZE 240
#define D2R_ABI_FRAME_SIZE 256

#ifndef __ASSEMBLER__
#include <stddef.h>
#include <stdint.h>

typedef struct {
    uint64_t x[9];
    uint64_t nzcv;
    uint8_t q[8][16];
    uint64_t original_x8;
    uint64_t target;
    uint64_t kind;
    uint64_t reserved;
} d2r_probe_abi;

_Static_assert(sizeof(d2r_probe_abi) == D2R_ABI_STATE_SIZE, "shim ABI size");
_Static_assert(offsetof(d2r_probe_abi, q) == D2R_ABI_Q0, "shim vectors ABI");
_Static_assert(offsetof(d2r_probe_abi, original_x8) == D2R_ABI_ORIGINAL_X8, "shim sret ABI");
void d2r_probe_before(d2r_probe_abi *state);
void d2r_probe_after(d2r_probe_abi *state);
void d2r_probe_server_send(void);
void d2r_probe_client_send(void);
void d2r_probe_system_pop(void);
void d2r_probe_gameplay_pop(void);
void d2r_probe_client_high_pop(void);
void d2r_probe_client_low_pop(void);
#endif
#endif
