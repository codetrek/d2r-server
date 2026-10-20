#include "internal.h"
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <mach/mach.h>
#include <mach/mach_vm.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

typedef struct {
    d2r_trace_header *header;
    size_t capacity;
    int fd;
    uint8_t *server, *client;
    uintptr_t targets[D2R_TRACE_KIND_COUNT];
    d2r_probe_frame_fn frame;
    unsigned phase;
    uint64_t inflight;
    char *stop_path;
    pthread_t control_thread;
    int control_started;
} probe_context;
static probe_context context = {.fd = -1};
static _Thread_local unsigned observing;
static pthread_mutex_t window_lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t window_changed = PTHREAD_COND_INITIALIZER;
enum { WINDOW_OPEN, WINDOW_CLOSING, WINDOW_CLOSED };
enum { CALL_ADMITTED = 1u, CALL_OBSERVE_POSTPOP = 2u };

static uint64_t thread_id(void) {
    uint64_t id = 0;
    if (pthread_threadid_np(NULL, &id)) return 0;
    return id;
}
static void increment(uint64_t *field) { __atomic_fetch_add(field, 1, __ATOMIC_RELAXED); }
static void failure(void) {
    increment(&context.header->errors);
    __atomic_fetch_or(&context.header->flags, D2R_TRACE_CAPTURE_GAP, __ATOMIC_RELAXED);
}

/* Region checks bound observation to readable local mappings; they do not add
 * synchronization to Guest allocation lifetimes. Captures remain on the owner. */
static int readable(uintptr_t pointer, size_t length) {
    if (!length) return 1;
    if (pointer > UINTPTR_MAX - length) return 0;
    uintptr_t end = pointer + length;
    while (pointer < end) {
        mach_vm_address_t address = pointer;
        mach_vm_size_t size = 0;
        vm_region_basic_info_data_64_t info;
        mach_msg_type_number_t count = VM_REGION_BASIC_INFO_COUNT_64;
        mach_port_t object = MACH_PORT_NULL;
        kern_return_t result = mach_vm_region(mach_task_self(), &address, &size,
            VM_REGION_BASIC_INFO_64, (vm_region_info_t)&info, &count, &object);
        if (object != MACH_PORT_NULL) mach_port_deallocate(mach_task_self(), object);
        if (result != KERN_SUCCESS || address > pointer || !size ||
            !(info.protection & VM_PROT_READ) || address > UINTPTR_MAX - size) return 0;
        uintptr_t next = address + size;
        if (next <= pointer) return 0;
        pointer = next < end ? next : end;
    }
    return 1;
}

static void record(uint16_t kind, uint16_t flags, uint32_t cid, uint32_t mode,
                   const uint8_t *payload, size_t length, uint64_t original_length) {
    d2r_trace_header *h = context.header;
    size_t size = (D2R_TRACE_RECORD_HEADER_SIZE + length + 7u) & ~(size_t)7u;
    uint64_t at = __atomic_load_n(&h->committed, __ATOMIC_RELAXED);
    if (size > context.capacity || at > context.capacity - size) {
        increment(&h->overflow); failure(); return;
    }
    d2r_trace_record item = {
        .record_size = (uint32_t)size, .kind = kind, .flags = flags,
        .sequence = h->records + 1, .frame = context.frame(),
        .thread = h->owner_thread, .client_id = cid, .mode = mode,
        .payload_length = (uint32_t)length, .original_length = original_length
    };
    uint8_t *destination = (uint8_t *)h + at;
    memcpy(destination, &item, sizeof item);
    if (length) memcpy(destination + sizeof item, payload, length);
    memset(destination + sizeof item + length, 0, size - sizeof item - length);
    h->records++;
    __atomic_store_n(&h->committed, at + size, __ATOMIC_RELEASE);
}

static void span_record(uint16_t kind, uint16_t flags, uint32_t cid, uint32_t mode,
                        uintptr_t begin, uintptr_t end) {
    if (end < begin) {
        failure(); record(kind, flags | D2R_RECORD_BAD_SPAN, cid, mode, NULL, 0, 0); return;
    }
    uint64_t length = end - begin;
    if (length > D2R_TRACE_OBSERVATION_CAP) {
        failure(); record(kind, flags | D2R_RECORD_CAP_EXCEEDED, cid, mode, NULL, 0, length); return;
    }
    if (!readable(begin, (size_t)length) ||
        ((kind == D2R_TRACE_SYSTEM_POSTPOP || kind == D2R_TRACE_GAMEPLAY_POSTPOP) && length < 4)) {
        failure(); record(kind, flags | D2R_RECORD_BAD_SPAN, cid, mode, NULL, 0, length); return;
    }
    if (kind == D2R_TRACE_SYSTEM_POSTPOP || kind == D2R_TRACE_GAMEPLAY_POSTPOP) {
        memcpy(&cid, (const void *)begin, 4); flags |= D2R_RECORD_CLIENT_ID;
    }
    record(kind, flags | D2R_RECORD_PAYLOAD, cid, mode, (const uint8_t *)begin, (size_t)length, length);
}
static void descriptor_record(uint16_t kind, uint16_t flags, uint32_t cid,
                              uint32_t mode, uintptr_t descriptor) {
    if (!readable(descriptor, 16)) {
        failure(); record(kind, flags | D2R_RECORD_BAD_SPAN, cid, mode, NULL, 0, 0); return;
    }
    uintptr_t span[2]; memcpy(span, (const void *)descriptor, sizeof span);
    span_record(kind, flags, cid, mode, span[0], span[1]);
}
static int enter_observation(void) {
    if (thread_id() != context.header->owner_thread) {
        increment(&context.header->thread_violations); failure(); return 0;
    }
    if (observing) { failure(); return 0; }
    observing = 1; return 1;
}

void d2r_probe_before(d2r_probe_abi *s) {
    int saved_errno = errno;
    unsigned kind = (unsigned)s->kind;
    s->target = context.targets[kind - 1];
    s->reserved = 0;
    pthread_mutex_lock(&window_lock);
    if (context.phase != WINDOW_OPEN) {
        pthread_mutex_unlock(&window_lock); errno = saved_errno; return;
    }
    context.inflight++; s->reserved = CALL_ADMITTED;
    pthread_mutex_unlock(&window_lock);
    if (kind <= D2R_TRACE_CLIENT_SEND_ATTEMPT) increment(&context.header->attempts);
    else increment(&context.header->pops);
    if (!enter_observation()) { errno = saved_errno; return; }
    uint8_t *expected = kind == D2R_TRACE_CLIENT_SEND_ATTEMPT || kind >= D2R_TRACE_CLIENT_HIGH_POSTPOP
        ? context.client : context.server;
    if ((uintptr_t)expected != s->x[0]) {
        failure(); record(kind, D2R_RECORD_BAD_SPAN, 0, 0, NULL, 0, 0);
    } else if (kind == D2R_TRACE_SERVER_SEND_ATTEMPT) {
        uint32_t cid = (uint32_t)s->x[1], mode;
        memcpy(&mode, context.client + 0x14, 4);
        /* The fixed original rejects these branches before dereferencing x2.
         * Preserve those no-op paths even when their descriptor is NULL.
         * https://github.com/jhaizhou-ops/d2r-mobile/blob/120fe55c02a5633476998a44f8c3c3c809fa48b0/dm/host/dm_elysium.c#L334 */
        if (cid || mode != 1 || context.client[0x2f0])
            record(kind, D2R_RECORD_CLIENT_ID | D2R_RECORD_IGNORED, cid, mode, NULL, 0, 0);
        else descriptor_record(kind, D2R_RECORD_CLIENT_ID, cid, mode, s->x[2]);
    } else if (kind == D2R_TRACE_CLIENT_SEND_ATTEMPT) {
        uint32_t mode; memcpy(&mode, context.client + 0x14, 4);
        if (mode == 1) descriptor_record(kind, 0, 0, mode, s->x[1]);
        else if (mode == 3) {
            failure(); record(kind, D2R_RECORD_CAPTURE_GAP, 0, mode, NULL, 0, 0);
        } else record(kind, D2R_RECORD_IGNORED, 0, mode, NULL, 0, 0);
    } else s->reserved |= CALL_OBSERVE_POSTPOP;
    observing = 0; errno = saved_errno;
}
static void finish_call(d2r_probe_abi *s) {
    pthread_mutex_lock(&window_lock);
    context.inflight--;
    if (!context.inflight) pthread_cond_broadcast(&window_changed);
    pthread_mutex_unlock(&window_lock);
    s->reserved = 0;
}
void d2r_probe_after(d2r_probe_abi *s) {
    if (!(s->reserved & CALL_ADMITTED)) return;
    int saved_errno = errno;
    if (!(s->reserved & CALL_OBSERVE_POSTPOP) || !enter_observation()) {
        finish_call(s); errno = saved_errno; return;
    }
    uintptr_t result = s->original_x8;
    uint8_t valid;
    if (result > UINTPTR_MAX - 16 || !readable(result + 16, 1)) {
        failure(); record(s->kind, D2R_RECORD_BAD_SPAN, 0, 0, NULL, 0, 0);
    } else {
        /* Empty optional results initialize only this byte. AAPCS64 carries the
         * original destination in x8, which the callee need not preserve:
         * https://github.com/ARM-software/abi-aa/blob/17f2cc8ee6645c1709088f4e0cb4438ce75a14ff/aapcs64/aapcs64.rst#result-return */
        memcpy(&valid, (const void *)(result + 16), 1);
        if (!valid) {
            increment(&context.header->empty_pops);
            record(s->kind, D2R_RECORD_EMPTY, 0, 0, NULL, 0, 0);
        } else if (valid == 1) descriptor_record(s->kind, 0, 0, 0, result);
        else { failure(); record(s->kind, D2R_RECORD_BAD_SPAN, 0, 0, NULL, 0, 0); }
    }
    observing = 0; finish_call(s); errno = saved_errno;
}

int d2r_probe_trace_start(const char *path, size_t capacity, void *server, void *client,
                          const uintptr_t targets[D2R_TRACE_KIND_COUNT], d2r_probe_frame_fn frame) {
    if (context.header || !path || !*path || capacity < D2R_TRACE_HEADER_SIZE ||
        capacity > D2R_TRACE_CAPACITY || !frame) { errno = EINVAL; return -1; }
    int fd = open(path, O_RDWR | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
    if (fd < 0) return -1;
    if (fchmod(fd, 0600)) { int e = errno; close(fd); errno = e; return -1; }
    if (ftruncate(fd, (off_t)capacity)) { int e = errno; close(fd); errno = e; return -1; }
    void *mapping = mmap(NULL, capacity, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
    if (mapping == MAP_FAILED) { int e = errno; close(fd); errno = e; return -1; }
    context = (probe_context){.header = mapping, .capacity = capacity, .fd = fd,
        .server = server, .client = client, .frame = frame};
    memcpy(context.targets, targets, sizeof context.targets);
    d2r_trace_header *h = mapping;
    memcpy(h->magic, D2R_TRACE_MAGIC, 8); h->version = D2R_TRACE_VERSION;
    h->header_size = D2R_TRACE_HEADER_SIZE; h->capacity = capacity;
    h->committed = D2R_TRACE_HEADER_SIZE; h->owner_thread = thread_id();
    h->observation_cap = D2R_TRACE_OBSERVATION_CAP;
    memcpy(h->main_sha256, d2r_main_hash, 32); memcpy(h->sdk_sha256, d2r_sdk_hash, 32);
    if (!h->owner_thread) { failure(); d2r_probe_close(); errno = EINVAL; return -1; }
    return 0;
}
void d2r_probe_installed(void) {
    __atomic_fetch_or(&context.header->flags, D2R_TRACE_INSTALLED | D2R_TRACE_PROFILE_MATCHED, __ATOMIC_RELEASE);
}
void d2r_probe_close(void) {
    pthread_mutex_lock(&window_lock);
    if (!context.header || context.phase == WINDOW_CLOSED) {
        pthread_mutex_unlock(&window_lock); return;
    }
    if (context.phase == WINDOW_CLOSING) {
        while (context.phase != WINDOW_CLOSED) pthread_cond_wait(&window_changed, &window_lock);
        pthread_mutex_unlock(&window_lock); return;
    }
    context.phase = WINDOW_CLOSING;
    while (context.inflight) pthread_cond_wait(&window_changed, &window_lock);
    pthread_mutex_unlock(&window_lock);
    int failed = 0;
    if (msync(context.header, context.capacity, MS_SYNC)) {
        failure(); failed = 1; fputs("d2r probe: trace sync failed\n", stderr);
    }
    if (close(context.fd)) {
        failure(); failed = 1; fputs("d2r probe: trace descriptor close failed\n", stderr);
    }
    /* CLOSED is published only after admitted originals and post-pop copies
     * finish and their file pages have been synced. The mapping and targets
     * remain alive; later calls are outside this explicitly bounded interval. */
    if (!failed) __atomic_fetch_or(&context.header->flags, D2R_TRACE_CLOSED, __ATOMIC_RELEASE);
    pthread_mutex_lock(&window_lock);
    context.phase = WINDOW_CLOSED;
    pthread_cond_broadcast(&window_changed);
    pthread_mutex_unlock(&window_lock);
}

int d2r_probe_validate_stop(const char *path, const char *trace_path) {
    if (!path || path[0] != '/' || strlen(path) >= PATH_MAX ||
        !trace_path || !strcmp(path, trace_path)) { errno = EINVAL; return -1; }
    struct stat info;
    if (!lstat(path, &info)) { errno = EEXIST; return -1; }
    return errno == ENOENT ? 0 : -1;
}
static void *control_main(void *unused) {
    (void)unused;
    for (;;) {
        pthread_mutex_lock(&window_lock);
        unsigned phase = context.phase;
        pthread_mutex_unlock(&window_lock);
        if (phase != WINDOW_OPEN) return NULL;
        int fd = open(context.stop_path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK);
        if (fd >= 0) {
            struct stat info;
            int valid = !fstat(fd, &info) && S_ISREG(info.st_mode) && info.st_uid == geteuid();
            if (close(fd)) valid = 0;
            if (!valid) { failure(); fputs("d2r probe: invalid stop marker\n", stderr); }
            d2r_probe_close(); return NULL;
        }
        if (errno != ENOENT && errno != EINTR) {
            failure(); fputs("d2r probe: stop marker lookup failed\n", stderr);
            d2r_probe_close(); return NULL;
        }
        usleep(20000);
    }
}
int d2r_probe_start_control(const char *path) {
    context.stop_path = strdup(path);
    if (!context.stop_path) { failure(); return -1; }
    int result = pthread_create(&context.control_thread, NULL, control_main, NULL);
    if (result) { free(context.stop_path); context.stop_path = NULL; failure(); errno = result; return -1; }
    context.control_started = 1;
#ifndef D2R_PROBE_TESTING
    result = pthread_detach(context.control_thread);
    if (result) { failure(); errno = result; return -1; }
#endif
    return 0;
}
#ifdef D2R_PROBE_TESTING
int d2r_probe_test_start(const char *path, size_t capacity, void *server, void *client,
                        const uintptr_t targets[D2R_TRACE_KIND_COUNT], d2r_probe_frame_fn frame) {
    return d2r_probe_trace_start(path, capacity, server, client, targets, frame);
}
int d2r_probe_test_control(const char *path) {
    if (d2r_probe_validate_stop(path, "unused-trace-path")) return -1;
    return d2r_probe_start_control(path);
}
unsigned d2r_probe_test_phase(void) {
    pthread_mutex_lock(&window_lock); unsigned phase = context.phase;
    pthread_mutex_unlock(&window_lock); return phase;
}
void d2r_probe_test_reset(void) {
    if (context.control_started) {
        if (pthread_join(context.control_thread, NULL)) abort();
    }
    pthread_mutex_lock(&window_lock);
    if (context.phase != WINDOW_CLOSED || context.inflight) abort();
    if (munmap(context.header, context.capacity)) abort();
    free(context.stop_path); context = (probe_context){.fd = -1};
    pthread_mutex_unlock(&window_lock);
}
#endif
