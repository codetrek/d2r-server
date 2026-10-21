#include "internal.h"
#include "lifecycle.h"
#include <errno.h>
#include <fcntl.h>
#include <mach/mach.h>
#include <mach/mach_vm.h>
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>
#ifdef D2R_PROBE_TESTING
#include <assert.h>
#endif

typedef struct {
    d2r_lifecycle_header *header;
    uint8_t *base;
    int fd, close_error;
    unsigned phase;
    uint64_t inflight;
    uint64_t (*frame)(void), (*sequence)(void);
} lifecycle_context;
static lifecycle_context lifecycle = {.fd = -1};
static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t changed = PTHREAD_COND_INITIALIZER;
static _Thread_local unsigned admitted_here;
enum { DORMANT, OPEN, CLOSING, CLOSED };
enum { ADMITTED = 1u, OWNER = 2u };

static uint64_t current_thread(void) {
    uint64_t id = 0;
    if (pthread_threadid_np(NULL, &id)) return 0;
    return id;
}
static void increment(uint64_t *value) { __atomic_fetch_add(value, 1, __ATOMIC_RELAXED); }
static void failure(void) {
    increment(&lifecycle.header->errors);
    __atomic_fetch_or(&lifecycle.header->flags, D2R_LIFE_GAP, __ATOMIC_RELAXED);
}
static uint32_t word(size_t offset) {
    uint32_t value; memcpy(&value, lifecycle.base + offset, sizeof value); return value;
}
static uint64_t wide(size_t offset) {
    uint64_t value; memcpy(&value, lifecycle.base + offset, sizeof value); return value;
}
static int copy_borrowed(uintptr_t address, void *destination, size_t size) {
    if (address > UINTPTR_MAX - size) return 0;
    mach_vm_size_t copied = 0;
    /* Kernel copying makes a changed mapping an explicit failed observation.
     * It does not establish ownership of a dynamically referenced object.
     * https://github.com/jhaizhou-ops/d2r-mobile/blob/120fe55c02a5633476998a44f8c3c3c809fa48b0/dm/host/diag.c#L361 */
    return mach_vm_read_overwrite(mach_task_self(), address, size,
        (mach_vm_address_t)destination, &copied) == KERN_SUCCESS && copied == size;
}

static void snapshot(unsigned gate, unsigned phase, const uint64_t *registers) {
    d2r_lifecycle_header *header = lifecycle.header;
    uint64_t at = __atomic_load_n(&header->committed, __ATOMIC_RELAXED);
    if (at > D2R_LIFE_CAPACITY - D2R_LIFE_RECORD_SIZE) {
        increment(&header->overflow); failure(); return;
    }
    d2r_lifecycle_record record = {
        .sequence = header->records + 1,
        .frame = lifecycle.frame(), .monotonic_ns = clock_gettime_nsec_np(CLOCK_MONOTONIC),
        .transport_sequence = lifecycle.sequence(), .gate = gate, .phase = phase,
        .operation_mode = word(0x2d20b8c), .state_b90 = word(0x2d20b90),
        .state_b94 = word(0x2d20b94), .state_b98 = word(0x2d20b98),
        .state_b9c = word(0x2d20b9c), .clock_e7c = word(0x2d20e7c),
        .error_code = word(0x2d20e80), .timer_1f0 = word(0x2d2d1f0),
        .timer_1f4 = word(0x2d2d1f4), .deferred_messages = word(0x2d435d0),
        .transport_mode = word(D2R_CLIENT_OFFSET + 0x14),
        .last_receive = wide(D2R_CLIENT_OFFSET + 0x28), .last_send = wide(D2R_CLIENT_OFFSET + 0x30),
        .error_deadline = wide(0x2d20fc0), .pending_present = wide(D2R_CLIENT_OFFSET + 0x50) != 0,
        .state_ba1 = lifecycle.base[0x2d20ba1], .frontend_error = lifecycle.base[0x2d2d1ec],
        .transport_closed = lifecycle.base[D2R_CLIENT_OFFSET + 0x2f0],
        .first_handle_present = wide(D2R_CLIENT_OFFSET + 0x18) != 0,
        .second_handle_present = wide(D2R_CLIENT_OFFSET + 0x20) != 0
    };
    uintptr_t parent = wide(0x2d20b80);
    if (parent) {
        record.flags |= D2R_LIFE_PARENT_PRESENT;
        uint32_t parent_value;
        if (parent > UINTPTR_MAX - 0x58 || !copy_borrowed(parent + 0x58, &parent_value, 4)) {
            record.flags |= D2R_LIFE_PARENT_UNREADABLE; failure();
        } else {
            record.parent_state_58 = parent_value;
            record.flags |= D2R_LIFE_PARENT_STATE;
        }
    }
    if (gate == D2R_LIFE_GATE_UI) {
        uintptr_t message = registers[1];
        uint64_t metadata[3];
        if (!copy_borrowed(message, metadata, sizeof metadata)) {
            record.flags |= D2R_LIFE_UI_UNREADABLE; failure();
        } else {
            record.ui_category = metadata[0];
            record.ui_action = metadata[1];
            record.flags |= D2R_LIFE_UI_METADATA;
        }
    }
    memcpy((uint8_t *)header + at, &record, sizeof record);
    header->records++;
    __atomic_store_n(&header->committed, at + sizeof record, __ATOMIC_RELEASE);
}

int d2r_lifecycle_start(uint8_t *base, size_t size, const char *path,
                       uint64_t (*frame)(void), uint64_t (*sequence)(void)) {
    if (lifecycle.header || !base || size != D2R_MAIN_SIZE ||
        (uintptr_t)base > UINTPTR_MAX - size || !path || path[0] != '/' || !frame || !sequence) {
        errno = EINVAL; return -1;
    }
    uint64_t owner = current_thread();
    if (!owner) { errno = EINVAL; return -1; }
    int fd = open(path, O_RDWR | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
    if (fd < 0) return -1;
    if (fchmod(fd, 0600) || ftruncate(fd, D2R_LIFE_CAPACITY)) {
        int error = errno; close(fd); errno = error; return -1;
    }
    void *mapping = mmap(NULL, D2R_LIFE_CAPACITY, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
    if (mapping == MAP_FAILED) { int error = errno; close(fd); errno = error; return -1; }
    d2r_lifecycle_header *header = mapping;
    memcpy(header->magic, D2R_LIFE_MAGIC, 8);
    header->version = D2R_LIFE_VERSION; header->header_size = D2R_LIFE_HEADER_SIZE;
    header->capacity = D2R_LIFE_CAPACITY; header->committed = D2R_LIFE_HEADER_SIZE;
    header->record_size = D2R_LIFE_RECORD_SIZE; header->owner_thread = owner;
    header->started_ns = clock_gettime_nsec_np(CLOCK_MONOTONIC); header->flags = D2R_LIFE_STARTED;
    memcpy(header->main_sha256, d2r_main_hash, 32); memcpy(header->sdk_sha256, d2r_sdk_hash, 32);
    pthread_mutex_lock(&lock);
    lifecycle = (lifecycle_context){.header = mapping, .base = base, .fd = fd, .phase = OPEN,
        .frame = frame, .sequence = sequence};
    pthread_mutex_unlock(&lock);
    return 0;
}
void d2r_lifecycle_profile_matched(void) {
    __atomic_fetch_or(&lifecycle.header->flags, D2R_LIFE_PROFILE_MATCHED, __ATOMIC_RELEASE);
}
unsigned d2r_lifecycle_enter(unsigned gate, const uint64_t *registers) {
    int saved_errno = errno;
    pthread_mutex_lock(&lock);
    if (!lifecycle.header || lifecycle.phase != OPEN) {
        pthread_mutex_unlock(&lock); errno = saved_errno; return 0;
    }
    lifecycle.inflight++; increment(&lifecycle.header->callbacks);
    pthread_mutex_unlock(&lock);
    admitted_here++;
    if (current_thread() != lifecycle.header->owner_thread) {
        increment(&lifecycle.header->thread_violations); failure(); errno = saved_errno; return ADMITTED;
    }
    snapshot(gate, 0, registers); errno = saved_errno; return ADMITTED | OWNER;
}
void d2r_lifecycle_leave(unsigned token, unsigned gate, const uint64_t *registers) {
    if (!(token & ADMITTED)) return;
    int saved_errno = errno;
    if (token & OWNER) snapshot(gate, 1, registers);
    pthread_mutex_lock(&lock);
    lifecycle.inflight--;
    admitted_here--;
    if (!lifecycle.inflight) pthread_cond_broadcast(&changed);
    pthread_mutex_unlock(&lock);
    errno = saved_errno;
}
int d2r_lifecycle_close(void) {
    pthread_mutex_lock(&lock);
    if (admitted_here) {
        failure(); pthread_mutex_unlock(&lock); errno = EDEADLK; return -1;
    }
    if (!lifecycle.header || lifecycle.phase == CLOSED) {
        int result = lifecycle.header && !(lifecycle.header->flags & D2R_LIFE_CLOSED) ? -1 : 0;
        if (result) errno = lifecycle.close_error;
        pthread_mutex_unlock(&lock); return result;
    }
    if (lifecycle.phase == CLOSING) {
        while (lifecycle.phase != CLOSED) pthread_cond_wait(&changed, &lock);
        int result = lifecycle.header->flags & D2R_LIFE_CLOSED ? 0 : -1;
        if (result) errno = lifecycle.close_error;
        pthread_mutex_unlock(&lock); return result;
    }
    lifecycle.phase = CLOSING;
    while (lifecycle.inflight) pthread_cond_wait(&changed, &lock);
    pthread_mutex_unlock(&lock);
    lifecycle.header->closed_ns = clock_gettime_nsec_np(CLOCK_MONOTONIC);
    int close_error = 0;
    if (msync(lifecycle.header, D2R_LIFE_CAPACITY, MS_SYNC)) { close_error = errno; failure(); }
    if (close(lifecycle.fd)) { if (!close_error) close_error = errno; failure(); }
    if (!close_error) __atomic_fetch_or(&lifecycle.header->flags, D2R_LIFE_CLOSED, __ATOMIC_RELEASE);
    else fputs("d2r lifecycle: capture close failed\n", stderr);
    pthread_mutex_lock(&lock); lifecycle.fd = -1; lifecycle.close_error = close_error; lifecycle.phase = CLOSED;
    pthread_cond_broadcast(&changed); pthread_mutex_unlock(&lock);
    if (close_error) { errno = close_error; return -1; }
    return 0;
}
int d2r_lifecycle_callback_active(void) { return admitted_here != 0; }
#ifdef D2R_PROBE_TESTING
void d2r_lifecycle_test_break_descriptor(void) { assert(!close(lifecycle.fd)); }
unsigned d2r_lifecycle_test_phase(void) {
    pthread_mutex_lock(&lock); unsigned value = lifecycle.phase; pthread_mutex_unlock(&lock); return value;
}
void d2r_lifecycle_test_reset(void) {
    d2r_lifecycle_close();
    if (lifecycle.header) munmap(lifecycle.header, D2R_LIFE_CAPACITY);
    lifecycle = (lifecycle_context){.fd = -1};
}
#endif
