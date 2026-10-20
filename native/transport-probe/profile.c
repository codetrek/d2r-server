#include "internal.h"
#include <CommonCrypto/CommonDigest.h>
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

const uint8_t d2r_main_hash[32] = {0x30, 0x38, 0x4e, 0xab, 0xe3, 0x94, 0xcb, 0xc6, 0xb6, 0xa0, 0xb7, 0xf3, 0x78, 0x89, 0x83, 0xd5, 0x8d, 0xc1, 0x4a, 0x92, 0xb7, 0xab, 0xe1, 0x63, 0x88, 0x53, 0xdc, 0x48, 0x33, 0xcf, 0x81, 0xf1};
const uint8_t d2r_sdk_hash[32] = {0xd4, 0x8f, 0x2c, 0xc3, 0x3f, 0x3e, 0x50, 0x6d, 0x62, 0x3f, 0x87, 0x3b, 0xa4, 0x47, 0xc8, 0x8f, 0xe0, 0xae, 0xa9, 0x89, 0xb9, 0x8c, 0x40, 0x57, 0xe2, 0x48, 0x1b, 0x6c, 0xa3, 0x98, 0x8f, 0xa2};
static const uintptr_t server_slots[14] = {0x84becc, 0x84c968, 0x84c98c, 0x84cfb0, 0x84d088, 0x84cdd8, 0x84ce48, 0x84d1c0, 0x84d560, 0x84d694, 0x84d6cc, 0x84d778, 0x83ed18, 0x83f154};
static const uintptr_t client_slots[26] = {0x8401c0, 0x840290, 0x83fe64, 0x83fe9c, 0x8404d4, 0x841568, 0x840354, 0x841670, 0x841720, 0x841740, 0x83f1c0, 0x83fd98, 0x84bd28, 0x84bb78, 0x84b3c0, 0x84b4f4, 0x84b66c, 0x84b82c, 0x84b834, 0x84ba54, 0x848554, 0x84847c, 0x84b334, 0x84b8dc, 0x841074, 0x8414e8};
static const uintptr_t secondary_slots[16] = {0x83fd90, 0x83fe04, 0x84b378, 0x84b458, 0x84b824, 0x84b830, 0x84b888, 0x84b664, 0x8484e8, 0x84bd20, 0x84b32c, 0x84b994, 0x84bae4, 0x84bec4, 0x8414d8, 0x841528};

static uintptr_t server_clone[16], client_clone[28];

static void prepare_clones(const uintptr_t *server, const uintptr_t *client, uintptr_t targets[4]) {
    memcpy(server_clone, server, sizeof server_clone);
    memcpy(client_clone, client, sizeof client_clone);
    targets[0] = server_clone[2 + 5]; targets[1] = client_clone[2 + 6];
    targets[2] = server_clone[2 + 10]; targets[3] = server_clone[2 + 11];
    server_clone[2 + 5] = (uintptr_t)d2r_probe_server_send;
    client_clone[2 + 6] = (uintptr_t)d2r_probe_client_send;
    server_clone[2 + 10] = (uintptr_t)d2r_probe_system_pop;
    server_clone[2 + 11] = (uintptr_t)d2r_probe_gameplay_pop;
}

static int digest_matches(int directory, const char *name, size_t expected_size,
                          const uint8_t expected[32]) {
    int fd = openat(directory, name, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) return -1;
    struct stat first, last;
    int error = 0;
    if (fstat(fd, &first)) error = errno;
    else if (!S_ISREG(first.st_mode) || first.st_size != (off_t)expected_size) error = EINVAL;
    CC_SHA256_CTX hash;
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
    CC_SHA256_Init(&hash);
    uint8_t buffer[65536], digest[CC_SHA256_DIGEST_LENGTH];
    while (!error) {
        ssize_t length = read(fd, buffer, sizeof buffer);
        if (length < 0) { if (errno == EINTR) continue; error = errno; break; }
        if (!length) break;
        CC_SHA256_Update(&hash, buffer, (CC_LONG)length);
    }
    CC_SHA256_Final(digest, &hash);
#pragma clang diagnostic pop
    if (!error && fstat(fd, &last)) error = errno;
    if (!error && (first.st_size != last.st_size || first.st_mtimespec.tv_sec != last.st_mtimespec.tv_sec ||
        first.st_mtimespec.tv_nsec != last.st_mtimespec.tv_nsec || memcmp(digest, expected, 32))) error = EINVAL;
    if (close(fd) && !error) error = errno;
    if (error) { errno = error; return -1; }
    return 0;
}
static int module_bounds(const d2r_probe_module *m, size_t size) {
    return m && m->base && m->size == size && (uintptr_t)m->base <= UINTPTR_MAX - size;
}
static int table_matches(uint8_t *base, uintptr_t offset, const uintptr_t *slots,
                         size_t count, intptr_t top) {
    uintptr_t *table = (uintptr_t *)(base + offset);
    if ((intptr_t)table[-2] != top || table[-1] != 0) return 0;
    for (size_t i = 0; i < count; i++)
        if (table[i] != (uintptr_t)base + slots[i]) return 0;
    return 1;
}
static int runtime_matches(const d2r_probe_module *modules, size_t count, uint8_t **base) {
    const d2r_probe_module *main = NULL, *sdk = NULL;
    if (!modules || count != 2) { errno = EINVAL; return -1; }
    for (size_t i = 0; i < count; i++) {
        if (!modules[i].name) { errno = EINVAL; return -1; }
        if (!strcmp(modules[i].name, "main") && !main) main = &modules[i];
        else if (!strcmp(modules[i].name, "sdk") && !sdk) sdk = &modules[i];
        else { errno = EINVAL; return -1; }
    }
    if (!module_bounds(main, D2R_MAIN_SIZE) || !module_bounds(sdk, D2R_SDK_SIZE)) { errno = EINVAL; return -1; }
    uint8_t *b = main->base;
    if (*(uintptr_t *)(b + D2R_SERVER_OFFSET) != (uintptr_t)b + D2R_SERVER_TABLE ||
        *(uintptr_t *)(b + D2R_CLIENT_OFFSET) != (uintptr_t)b + D2R_CLIENT_TABLE ||
        *(uintptr_t *)(b + D2R_CLIENT_OFFSET + 8) != (uintptr_t)b + D2R_CLIENT_SECONDARY_TABLE ||
        !table_matches(b, D2R_SERVER_TABLE, server_slots, 14, 0) ||
        !table_matches(b, D2R_CLIENT_TABLE, client_slots, 26, 0) ||
        !table_matches(b, D2R_CLIENT_SECONDARY_TABLE, secondary_slots, 16, -8)) { errno = EINVAL; return -1; }
    *base = b;
    return 0;
}
int d2r_probe_install(const d2r_probe_module *modules, size_t count,
                      const char *directory, const char *trace_path, const char *stop_path, d2r_probe_frame_fn frame) {
    if (!directory || !*directory || !trace_path || !*trace_path) { errno = EINVAL; return -1; }
    if (d2r_probe_validate_stop(stop_path, trace_path)) return -1;
    int dir = open(directory, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW);
    if (dir < 0) return -1;
    int result = digest_matches(dir, "main.patched.bin", D2R_MAIN_SIZE, d2r_main_hash);
    if (!result) result = digest_matches(dir, "sdk.patched.bin", D2R_SDK_SIZE, d2r_sdk_hash);
    int error = errno;
    if (close(dir) && !result) { result = -1; error = errno; }
    if (result) { errno = error; return -1; }
    uint8_t *base;
    if (runtime_matches(modules, count, &base)) return -1;
    /* Address points have negative Itanium headers. Preserve the complete known
     * tables and leave the secondary subobject's original vptr untouched.
     * https://github.com/itanium-cxx-abi/cxx-abi/blob/fae5e46c86147a457a057c6372fa95ec38209fe8/abi.html#L1284 */
    uintptr_t targets[4];
    prepare_clones((uintptr_t *)(base + D2R_SERVER_TABLE - 16),
                   (uintptr_t *)(base + D2R_CLIENT_TABLE - 16), targets);
    if (d2r_probe_trace_start(trace_path, D2R_TRACE_CAPACITY, base + D2R_SERVER_OFFSET,
                              base + D2R_CLIENT_OFFSET, targets, frame)) return -1;
    __atomic_store_n((uintptr_t *)(base + D2R_SERVER_OFFSET), (uintptr_t)(server_clone + 2), __ATOMIC_RELEASE);
    __atomic_store_n((uintptr_t *)(base + D2R_CLIENT_OFFSET), (uintptr_t)(client_clone + 2), __ATOMIC_RELEASE);
    d2r_probe_installed();
    return d2r_probe_start_control(stop_path);
}
#ifdef D2R_PROBE_TESTING
int d2r_probe_test_profile(const d2r_probe_module *modules, size_t count) {
    uint8_t *base;
    return runtime_matches(modules, count, &base);
}
int d2r_probe_test_digest(const char *directory, const char *name, size_t size,
                          const uint8_t expected[32]) {
    int fd = open(directory, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) return -1;
    int result = digest_matches(fd, name, size, expected), error = errno;
    if (close(fd) && !result) { result = -1; error = errno; }
    errno = error; return result;
}
void d2r_probe_test_clones(const uintptr_t server[16], const uintptr_t client[28],
                           uintptr_t server_result[16], uintptr_t client_result[28]) {
    uintptr_t targets[4]; prepare_clones(server, client, targets);
    memcpy(server_result, server_clone, sizeof server_clone);
    memcpy(client_result, client_clone, sizeof client_clone);
}
#endif
