#ifndef D2R_TRANSPORT_PROBE_INTERNAL_H
#define D2R_TRANSPORT_PROBE_INTERNAL_H
#include "probe.h"
#include "lifecycle.h"
#define D2R_SERVER_OFFSET 0x2d7f3d8u
#define D2R_CLIENT_OFFSET 0x2d9f438u
#define D2R_SERVER_TABLE 0x20afb40u
#define D2R_CLIENT_TABLE 0x20afbc0u
#define D2R_CLIENT_SECONDARY_TABLE 0x20afca0u
#define D2R_MAIN_SIZE 54378496u
#define D2R_SDK_SIZE 15204352u
extern const uint8_t d2r_main_hash[32], d2r_sdk_hash[32];
int d2r_probe_trace_start(const char *path, size_t capacity, void *server, void *client,
                           const uintptr_t targets[D2R_TRACE_KIND_COUNT], d2r_probe_frame_fn frame);
void d2r_probe_installed(void);
int d2r_probe_validate_stop(const char *path, const char *trace_path);
int d2r_probe_start_control(const char *path);
uint64_t d2r_probe_sequence(void);
int d2r_probe_open_captures(uint8_t *base, const char *trace_path,
                            const char *lifecycle_path, const char *stop_path,
                            const uintptr_t targets[D2R_TRACE_KIND_COUNT], d2r_probe_frame_fn frame);
#endif
