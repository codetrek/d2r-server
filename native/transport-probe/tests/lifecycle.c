#include "internal.h"
#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

extern void d2r_test_gate_fire(unsigned id, const uint64_t *registers);
extern void (*d2r_test_gate_registered(unsigned id))(const uint64_t *);
static uint8_t *module;
static pthread_t owner;
static unsigned number, first_calls, second_calls, tick_calls;
static char directory[4096], life_path[4096], trace_path[4096], stop_path[4096];
static uint64_t registers[32], message[] = {0x1122334455667788, 0x8877665544332211, 0};
static pthread_mutex_t blocking = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t block_changed = PTHREAD_COND_INITIALIZER;
static unsigned entered, release_original;

static uint64_t frame(void) { assert(pthread_equal(owner, pthread_self())); return 456; }
static uint64_t sequence(void) { assert(pthread_equal(owner, pthread_self())); return 789; }
static void set_word(size_t offset, uint32_t value) { memcpy(module + offset, &value, 4); }
static void set_wide(size_t offset, uint64_t value) { memcpy(module + offset, &value, 8); }
static void first(const uint64_t *value) { (void)value; first_calls++; errno = ERANGE; }
static void second(const uint64_t *value) { (void)value; second_calls++; errno = ERANGE; }
static void tick(const uint64_t *value) {
    assert(value == registers); tick_calls++; set_word(0x2d20b94, 77); errno = EOVERFLOW;
}
static void nested(const uint64_t *value) {
    assert(value == registers && errno == EDOM);
    first_calls++;
    ((uint64_t *)value)[7] = 0x12345678;
    set_word(0x2d20e80, 8);
    d2r_test_gate_fire(D2R_LIFE_GATE_TICK, registers);
    gate_obs_set(D2R_LIFE_GATE_UI, second);
    errno = ERANGE;
}
static void block_original(const uint64_t *value) {
    assert(value == registers);
    pthread_mutex_lock(&blocking); entered = 1;
    pthread_cond_broadcast(&block_changed);
    while (!release_original) pthread_cond_wait(&block_changed, &blocking);
    pthread_mutex_unlock(&blocking);
    set_word(0x2d20e80, 30); errno = ERANGE;
}
static void forbid_close(const uint64_t *value) {
    assert(value == registers);
    errno = 0; assert(d2r_lifecycle_close() == -1 && errno == EDEADLK);
    d2r_probe_close(); assert(errno == EDEADLK);
    second_calls++; errno = ERANGE;
}
static void start(void) {
    memset(module, 0, D2R_MAIN_SIZE);
    owner = pthread_self(); registers[1] = (uintptr_t)message;
    snprintf(life_path, sizeof life_path, "%s/life-%u.bin", directory, number++);
    assert(!d2r_lifecycle_start(module, D2R_MAIN_SIZE, life_path, frame, sequence));
}
static uint8_t *finish(d2r_lifecycle_header *header) {
    assert(!d2r_lifecycle_close());
    int fd = open(life_path, O_RDONLY); assert(fd >= 0);
    struct stat info; assert(!fstat(fd, &info) && info.st_size == D2R_LIFE_CAPACITY);
    assert((info.st_mode & 0777) == 0600);
    uint8_t *data = malloc(D2R_LIFE_CAPACITY); assert(data);
    assert(read(fd, data, D2R_LIFE_CAPACITY) == D2R_LIFE_CAPACITY && !close(fd));
    memcpy(header, data, sizeof *header); d2r_lifecycle_test_reset();
    assert(!memcmp(header->magic, D2R_LIFE_MAGIC, 8));
    assert(header->version == 1 && header->header_size == 256 && header->record_size == 160);
    assert(header->flags & D2R_LIFE_CLOSED && header->closed_ns >= header->started_ns);
    return data;
}
static void dormant_registry(void) {
    gate_obs_set(1, first); errno = EDOM; d2r_test_gate_fire(1, registers);
    assert(first_calls == 1 && errno == ERANGE);
    gate_obs_set(1, second); d2r_test_gate_fire(1, registers);
    assert(first_calls == 1 && second_calls == 1);
    void (*captured)(const uint64_t *) = d2r_test_gate_registered(1);
    gate_obs_set(1, NULL); assert(!d2r_test_gate_registered(1));
    captured(registers); assert(first_calls == 1 && second_calls == 1);
    gate_obs_set(3, first); assert(d2r_test_gate_registered(3) == first);
    d2r_test_gate_fire(3, registers); assert(first_calls == 2);
}
static void nested_snapshots(void) {
    start(); first_calls = second_calls = tick_calls = 0;
    uint8_t parent[96] = {0}; uint32_t marker = 0x13579bdf;
    memcpy(parent + 0x58, &marker, 4); set_wide(0x2d20b80, (uintptr_t)parent);
    set_wide(D2R_CLIENT_OFFSET + 0x50, (uintptr_t)parent);
    set_wide(0x2d20fc0, UINT64_C(0x123456789abcdef0));
    gate_obs_set(1, nested); gate_obs_set(2, tick);
    errno = EDOM; d2r_test_gate_fire(1, registers);
    assert(errno == ERANGE && registers[7] == 0x12345678);
    assert(first_calls == 1 && tick_calls == 1 && second_calls == 0);
    errno = EDOM; d2r_test_gate_fire(1, registers);
    assert(second_calls == 1 && errno == ERANGE);
    d2r_lifecycle_header header; uint8_t *data = finish(&header);
    assert(header.callbacks == 3 && header.records == 6 && !header.errors);
    assert(header.flags == (D2R_LIFE_STARTED | D2R_LIFE_CLOSED));
    d2r_lifecycle_record *records = (void *)(data + 256);
    assert(records[0].gate == 1 && records[0].phase == 0 && !records[0].error_code);
    assert(records[1].gate == 2 && records[1].phase == 0 && records[1].error_code == 8);
    assert(records[2].gate == 2 && records[2].phase == 1 && records[2].state_b94 == 77);
    assert(records[3].gate == 1 && records[3].phase == 1 && records[3].error_code == 8);
    for (unsigned i = 0; i < 6; i++) {
        assert(records[i].sequence == i + 1 && records[i].frame == 456 && records[i].transport_sequence == 789);
        assert(records[i].parent_state_58 == marker && records[i].pending_present == 1);
        assert(records[i].error_deadline == UINT64_C(0x123456789abcdef0));
        if (i) assert(records[i].monotonic_ns >= records[i - 1].monotonic_ns);
    }
    assert(records[0].ui_category == message[0] && records[0].ui_action == message[1]);
    assert(records[0].flags == (D2R_LIFE_UI_METADATA | D2R_LIFE_PARENT_PRESENT | D2R_LIFE_PARENT_STATE));
    assert(records[1].flags == (D2R_LIFE_PARENT_PRESENT | D2R_LIFE_PARENT_STATE));
    free(data);
}
static void *foreign(void *unused) { (void)unused; d2r_test_gate_fire(1, (const uint64_t *)1); return NULL; }
static void malformed_and_foreign(void) {
    start(); registers[1] = 1; gate_obs_set(1, second); d2r_test_gate_fire(1, registers);
    d2r_lifecycle_header header; uint8_t *data = finish(&header);
    assert(header.records == 2 && header.errors == 2 && header.flags & D2R_LIFE_GAP);
    d2r_lifecycle_record *record = (void *)(data + 256); assert(record->flags == D2R_LIFE_UI_UNREADABLE);
    free(data);
    start(); gate_obs_set(1, second); pthread_t thread;
    assert(!pthread_create(&thread, NULL, foreign, NULL) && !pthread_join(thread, NULL));
    data = finish(&header); assert(header.callbacks == 1 && !header.records && header.errors == 1 && header.thread_violations == 1);
    free(data);
    start(); set_wide(0x2d20b80, UINT64_MAX - 2); gate_obs_set(2, second); d2r_test_gate_fire(2, registers);
    data = finish(&header); record = (void *)(data + 256);
    assert(header.errors == 2 && record->flags == (D2R_LIFE_PARENT_PRESENT | D2R_LIFE_PARENT_UNREADABLE));
    free(data);
}
static void bounded_borrowed_memory(void) {
    size_t page = (size_t)sysconf(_SC_PAGESIZE);
    uint8_t *pages = mmap(NULL, 2 * page, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE, -1, 0);
    assert(pages != MAP_FAILED && !mprotect(pages + page, page, PROT_NONE));
    uint64_t *bounded = (void *)(pages + page - 24);
    memcpy(bounded, message, 24); bounded[2] = 1;
    start(); registers[1] = (uintptr_t)bounded;
    gate_obs_set(1, second); d2r_test_gate_fire(1, registers);
    d2r_lifecycle_header header; uint8_t *data = finish(&header);
    assert(!header.errors && header.records == 2);
    free(data);
    start(); registers[1] = (uintptr_t)(pages + page - 16); d2r_test_gate_fire(1, registers);
    data = finish(&header); assert(header.errors == 2); free(data);
    assert(!munmap(pages, 2 * page));
}
static void overflow_and_late_calls(void) {
    start(); gate_obs_set(2, second); unsigned calls = second_calls;
    for (unsigned i = 0; i < 8193; i++) d2r_test_gate_fire(2, registers);
    assert(!d2r_lifecycle_close() && second_calls == calls + 8193);
    int fd = open(life_path, O_RDONLY); assert(fd >= 0);
    d2r_lifecycle_header before, after;
    assert(pread(fd, &before, sizeof before, 0) == sizeof before);
    assert(before.records == 16384 && before.overflow == 2 && before.errors == 2);
    gate_obs_set(1, second); d2r_test_gate_fire(1, (const uint64_t *)1);
    pthread_t thread; assert(!pthread_create(&thread, NULL, foreign, NULL) && !pthread_join(thread, NULL));
    assert(pread(fd, &after, sizeof after, 0) == sizeof after && !close(fd));
    assert(!memcmp(&before, &after, sizeof before)); d2r_lifecycle_test_reset();
}
static void wait_closing(void) {
    for (unsigned i = 0; i < 2000; i++) {
        if (d2r_lifecycle_test_phase() == 2) return;
        usleep(1000);
    }
    assert(!"lifecycle close did not wait for original callback");
}
static void *stop_both(void *unused) {
    (void)unused;
    pthread_mutex_lock(&blocking);
    while (!entered) pthread_cond_wait(&block_changed, &blocking);
    pthread_mutex_unlock(&blocking);
    int fd = open(stop_path, O_CREAT | O_EXCL | O_WRONLY, 0600); assert(fd >= 0 && !close(fd));
    wait_closing();
    fd = open(trace_path, O_RDONLY); assert(fd >= 0);
    d2r_trace_header trace; assert(read(fd, &trace, sizeof trace) == sizeof trace && !close(fd));
    assert(!(trace.flags & D2R_TRACE_CLOSED));
    pthread_mutex_lock(&blocking); release_original = 1;
    pthread_cond_broadcast(&block_changed); pthread_mutex_unlock(&blocking);
    return NULL;
}
static void barrier(void) {
    snprintf(trace_path, sizeof trace_path, "%s/transport.bin", directory);
    snprintf(stop_path, sizeof stop_path, "%s/stop", directory);
    uintptr_t targets[D2R_TRACE_KIND_COUNT] = {1, 1, 1, 1, 1, 1};
    assert(!d2r_probe_test_start(trace_path, 4096, module, module, targets, frame));
    start(); entered = release_original = 0; gate_obs_set(1, block_original);
    assert(!d2r_probe_test_control(stop_path));
    pthread_t stopper; assert(!pthread_create(&stopper, NULL, stop_both, NULL));
    d2r_test_gate_fire(1, registers); assert(errno == ERANGE);
    assert(!pthread_join(stopper, NULL)); d2r_probe_close();
    int fd = open(trace_path, O_RDONLY); assert(fd >= 0);
    d2r_trace_header trace; assert(read(fd, &trace, sizeof trace) == sizeof trace && !close(fd));
    assert(trace.flags & D2R_TRACE_CLOSED);
    d2r_lifecycle_header header; uint8_t *data = finish(&header);
    d2r_lifecycle_record *records = (void *)(data + 256);
    assert(header.records == 2 && records[1].error_code == 30);
    free(data); d2r_probe_test_reset();
}
static void self_close_and_close_failure(void) {
    snprintf(trace_path, sizeof trace_path, "%s/reentrant-transport.bin", directory);
    uintptr_t targets[D2R_TRACE_KIND_COUNT] = {1, 1, 1, 1, 1, 1};
    assert(!d2r_probe_test_start(trace_path, 4096, module, module, targets, frame));
    start(); gate_obs_set(1, forbid_close); d2r_test_gate_fire(1, registers);
    assert(errno == ERANGE);
    d2r_probe_close();
    int fd = open(trace_path, O_RDONLY); assert(fd >= 0);
    d2r_trace_header trace; assert(read(fd, &trace, sizeof trace) == sizeof trace && !close(fd));
    assert(trace.flags == (D2R_TRACE_CLOSED | D2R_TRACE_CAPTURE_GAP) && trace.errors == 1);
    d2r_lifecycle_header header; uint8_t *data = finish(&header);
    assert(header.errors == 1 && header.flags == (D2R_LIFE_STARTED | D2R_LIFE_CLOSED | D2R_LIFE_GAP));
    free(data); d2r_probe_test_reset();

    snprintf(trace_path, sizeof trace_path, "%s/failed-transport.bin", directory);
    assert(!d2r_probe_test_start(trace_path, 4096, module, module, targets, frame));
    start(); d2r_lifecycle_test_break_descriptor();
    d2r_probe_close();
    assert(d2r_lifecycle_close() == -1 && errno == EBADF);
    fd = open(trace_path, O_RDONLY); assert(fd >= 0);
    assert(read(fd, &trace, sizeof trace) == sizeof trace && !close(fd));
    assert(!(trace.flags & D2R_TRACE_CLOSED) && trace.flags & D2R_TRACE_CAPTURE_GAP && trace.errors == 1);
    d2r_probe_test_reset(); d2r_lifecycle_test_reset();
}
static void capture_path_aliases(void) {
    char parent[4096]; snprintf(parent, sizeof parent, "%s/sub", directory); assert(!mkdir(parent, 0700));
    char alias[4096]; snprintf(alias, sizeof alias, "%s/alias-parent", directory); assert(!symlink(".", alias));
    uintptr_t targets[D2R_TRACE_KIND_COUNT] = {1, 1, 1, 1, 1, 1};
    for (unsigned scenario = 0; scenario < 3; scenario++) {
        snprintf(trace_path, sizeof trace_path, "%s/alias-transport-%u.bin", directory, scenario);
        snprintf(life_path, sizeof life_path, "%s/alias-life-%u.bin", directory, scenario);
        if (scenario == 0) snprintf(stop_path, sizeof stop_path, "%s/sub/../alias-life-%u.bin", directory, scenario);
        if (scenario == 1) snprintf(stop_path, sizeof stop_path, "%s/alias-parent/alias-life-%u.bin", directory, scenario);
        if (scenario == 2) snprintf(stop_path, sizeof stop_path, "%s/sub/../alias-transport-%u.bin", directory, scenario);
        assert(d2r_probe_open_captures(module, trace_path, life_path, stop_path, targets, frame) == -1 && errno == EEXIST);
        int fd = open(trace_path, O_RDONLY); assert(fd >= 0);
        d2r_trace_header trace; assert(read(fd, &trace, sizeof trace) == sizeof trace && !close(fd));
        assert(!(trace.flags & D2R_TRACE_INSTALLED));
        d2r_probe_test_reset(); d2r_lifecycle_test_reset();
    }
}
int main(int argc, char **argv) {
    setvbuf(stdout, NULL, _IONBF, 0); assert(argc == 2);
    snprintf(directory, sizeof directory, "%s/lifecycle-XXXXXX", argv[1]); assert(mkdtemp(directory));
    module = mmap(NULL, D2R_MAIN_SIZE, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE, -1, 0);
    assert(module != MAP_FAILED);
    dormant_registry(); puts("PASS dormant/latest/NULL/unselected callback registration");
    nested_snapshots(); puts("PASS same-register pointer, original errno, nested and replaced callback snapshots");
    malformed_and_foreign(); puts("PASS unreadable UI/parent and foreign-thread forwarding with explicit gaps");
    bounded_borrowed_memory(); puts("PASS exact UI metadata range and inaccessible argument are not overread");
    overflow_and_late_calls(); puts("PASS bounded capacity and no new capture after closure");
    barrier(); puts("PASS transport closure waits for complete lifecycle callback and post-snapshot");
    self_close_and_close_failure(); puts("PASS callback close rejection and lifecycle I/O failure invalidates transport closure");
    capture_path_aliases(); puts("PASS stop aliases through parent traversal/symlinks fail before hook publication");
    assert(!munmap(module, D2R_MAIN_SIZE)); return 0;
}
