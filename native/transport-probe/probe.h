#ifndef D2R_TRANSPORT_PROBE_H
#define D2R_TRANSPORT_PROBE_H
#include "abi.h"
#include "format.h"

typedef struct { const char *name; uint8_t *base; size_t size; } d2r_probe_module;
typedef uint64_t (*d2r_probe_frame_fn)(void);
int d2r_probe_install(const d2r_probe_module *modules, size_t count,
                      const char *module_directory, const char *trace_path, const char *stop_path,
                      const char *lifecycle_path,
                      const char *mode, const char *relay_read_fd, const char *relay_write_fd,
                      d2r_probe_frame_fn frame);
void d2r_probe_close(void);

#ifdef D2R_PROBE_TESTING
int d2r_probe_test_start(const char *path, size_t capacity, void *server,
                         void *client, const uintptr_t targets[D2R_TRACE_KIND_COUNT], d2r_probe_frame_fn frame);
int d2r_probe_test_profile(const d2r_probe_module *modules, size_t count);
int d2r_probe_test_digest(const char *directory, const char *name, size_t size,
                          const uint8_t expected[32]);
int d2r_probe_test_control(const char *path);
unsigned d2r_probe_test_phase(void);
void d2r_probe_test_reset(void);
void d2r_probe_test_clones(const uintptr_t server[16], const uintptr_t client[28],
                           uintptr_t server_result[16], uintptr_t client_result[28]);
#endif
#endif
