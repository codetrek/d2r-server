#include "probe.h"
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
#include <sys/wait.h>
#include <unistd.h>

d2r_probe_abi d2r_test_seen;
uint64_t d2r_test_calls;
unsigned d2r_test_mode;
uintptr_t d2r_test_span[2];
extern void d2r_test_call(void (*shim)(void), d2r_probe_abi *state);
extern void d2r_test_original(void);
static uint8_t server[64], client[0x300];
static char directory[4096], path[4096];
static unsigned test_number;
static pthread_mutex_t block_lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t block_changed = PTHREAD_COND_INITIALIZER;
static int block_entered, block_release;
static char stop_path[4096];
static pthread_t frame_owner;
static uint64_t frame(void) { assert(pthread_equal(pthread_self(), frame_owner)); return 123; }

void d2r_test_block_until_release(void) {
    pthread_mutex_lock(&block_lock); block_entered = 1;
    pthread_cond_broadcast(&block_changed);
    while (!block_release) pthread_cond_wait(&block_changed, &block_lock);
    pthread_mutex_unlock(&block_lock);
}

static void start(size_t capacity) {
    memset(server, 0, sizeof server); memset(client, 0, sizeof client);
    uint32_t mode = 1; memcpy(client + 0x14, &mode, 4);
    d2r_test_calls = 0; d2r_test_mode = 0;
    frame_owner = pthread_self();
    snprintf(path, sizeof path, "%s/case-%u.trace", directory, test_number++);
    uintptr_t targets[D2R_TRACE_KIND_COUNT];
    for (unsigned i = 0; i < D2R_TRACE_KIND_COUNT; i++) targets[i] = (uintptr_t)d2r_test_original;
    assert(!d2r_probe_test_start(path, capacity, server, client, targets, frame));
}
static d2r_probe_abi input(unsigned kind, uintptr_t descriptor) {
    d2r_probe_abi value = {0};
    for (unsigned i = 0; i < 9; i++) value.x[i] = 0x10000 + i;
    for (unsigned q = 0; q < 8; q++) memset(value.q[q], 0x30 + q, 16);
    value.nzcv = 0x60000000;
    value.x[0] = (uintptr_t)(kind == 2 || kind >= 5 ? client : server);
    if (kind == 1) { value.x[1] = 0; value.x[2] = descriptor; }
    if (kind == 2) value.x[1] = descriptor;
    if (kind >= 3) value.x[8] = descriptor;
    return value;
}
static void invoke(unsigned kind, uintptr_t descriptor) {
    void (*shims[])(void) = {d2r_probe_server_send, d2r_probe_client_send,
                             d2r_probe_system_pop, d2r_probe_gameplay_pop,
                             d2r_probe_client_high_pop, d2r_probe_client_low_pop};
    d2r_probe_abi value = input(kind, descriptor), expected = value;
    uint64_t before = d2r_test_calls;
    errno = EDOM;
    d2r_test_call(shims[kind - 1], &value);
    assert(errno == EDOM && d2r_test_calls == before + 1);
    assert(!memcmp(d2r_test_seen.x, expected.x, sizeof expected.x));
    assert(!memcmp(d2r_test_seen.q, expected.q, sizeof expected.q));
    assert(d2r_test_seen.nzcv == expected.nzcv);
    for (unsigned i = 0; i < 9; i++) assert(value.x[i] == (i + 1) * 0x101);
    for (unsigned q = 0; q < 8; q++) for (unsigned b = 0; b < 16; b++) assert(value.q[q][b] == 0xa0 + q);
    assert(value.nzcv == 0xa0000000);
}
static uint8_t *finish(d2r_trace_header *header) {
    d2r_probe_close();
    int fd = open(path, O_RDONLY); assert(fd >= 0);
    struct stat info; assert(!fstat(fd, &info));
    assert((info.st_mode & 0777) == 0600);
    uint8_t *data = malloc((size_t)info.st_size); assert(data);
    assert(read(fd, data, (size_t)info.st_size) == info.st_size); assert(!close(fd));
    memcpy(header, data, sizeof *header);
    d2r_probe_test_reset();
    assert(!memcmp(header->magic, D2R_TRACE_MAGIC, 8));
    assert(header->version == 2 && header->header_size == 256 && header->capacity == (uint64_t)info.st_size);
    assert(header->flags & D2R_TRACE_CLOSED);
    assert(header->owner_thread != 0);
    return data;
}
static void sends(void) {
    for (unsigned kind = 1; kind <= 2; kind++) {
        start(4096);
        uint8_t payload[] = {0x16, 0x80, 0xff};
        uintptr_t span[] = {(uintptr_t)payload, (uintptr_t)payload + sizeof payload};
        invoke(kind, (uintptr_t)span);
        memset(payload, 0, sizeof payload);
        d2r_trace_header h; uint8_t *data = finish(&h);
        d2r_trace_record *r = (void *)(data + 256);
        assert(h.records == 1 && h.attempts == 1 && !h.pops && !h.errors);
        assert(h.committed == 328 && r->record_size == 72 && r->sequence == 1);
        assert(r->kind == kind && r->frame == 123 && r->thread == h.owner_thread);
        assert(r->payload_length == 3 && r->original_length == 3 && r->flags & D2R_RECORD_PAYLOAD);
        assert(!memcmp(data + 320, (uint8_t[]){0x16, 0x80, 0xff}, 3));
        for (unsigned i = 323; i < 328; i++) assert(data[i] == 0);
        free(data);
    }
    start(4096); uintptr_t empty[] = {0, 0}; invoke(2, (uintptr_t)empty);
    d2r_trace_header h; uint8_t *data = finish(&h);
    d2r_trace_record *r = (void *)(data + 256);
    assert(!h.errors && r->flags == D2R_RECORD_PAYLOAD && !r->payload_length);
    free(data);
}
static void ignored_and_gaps(void) {
    for (unsigned scenario = 0; scenario < 5; scenario++) {
        start(4096);
        d2r_probe_abi state = input(scenario >= 3 ? 2 : 1, 1);
        if (scenario == 0) state.x[1] = 42;
        if (scenario == 1 || scenario == 3) memset(client + 0x14, 0, 4);
        if (scenario == 2) client[0x2f0] = 1;
        if (scenario == 4) { uint32_t mode = 3; memcpy(client + 0x14, &mode, 4); }
        d2r_test_call(scenario >= 3 ? d2r_probe_client_send : d2r_probe_server_send, &state);
        assert(d2r_test_calls == 1);
        d2r_trace_header h; uint8_t *data = finish(&h);
        d2r_trace_record *r = (void *)(data + 256);
        assert(r->payload_length == 0);
        if (scenario == 4) assert(h.errors == 1 && h.flags & D2R_TRACE_CAPTURE_GAP && r->flags == D2R_RECORD_CAPTURE_GAP);
        else assert(!h.errors && r->flags & D2R_RECORD_IGNORED);
        free(data);
    }
}
static void pops(void) {
    for (unsigned kind = 3; kind <= D2R_TRACE_KIND_COUNT; kind++) {
        start(4096);
        uint8_t payload[] = {0x12, 0x34, 0x56, 0x78, 0x67, 0xa5};
        if (kind == 5) payload[0] = 0xaf;
        uint8_t output[24]; memset(output, 0xcc, sizeof output);
        size_t length = kind <= 4 ? sizeof payload : kind == 5 ? 1 : 3;
        uint8_t expected[sizeof payload]; memcpy(expected, payload, sizeof payload);
        d2r_test_span[0] = (uintptr_t)payload; d2r_test_span[1] = (uintptr_t)payload + length;
        d2r_test_mode = 1;
        invoke(kind, (uintptr_t)output);
        assert(output[16] == 1 && output[17] == 0xcc);
        memset(payload, 0, sizeof payload);
        d2r_trace_header h; uint8_t *data = finish(&h);
        d2r_trace_record *r = (void *)(data + 256);
        assert(h.pops == 1 && !h.empty_pops && !h.errors && h.attempts == 0);
        assert(r->kind == kind && r->flags == (D2R_RECORD_PAYLOAD | (kind <= 4 ? D2R_RECORD_CLIENT_ID : 0)));
        assert(r->client_id == (kind <= 4 ? 0x78563412u : 0u) && r->payload_length == length);
        assert(!memcmp(data + 320, expected, length));
        free(data);
    }
    size_t page = (size_t)sysconf(_SC_PAGESIZE);
    uint8_t *guard = mmap(NULL, page * 2, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANON, -1, 0);
    assert(guard != MAP_FAILED && !mprotect(guard, page, PROT_NONE));
    for (unsigned kind = 3; kind <= D2R_TRACE_KIND_COUNT; kind++) {
        start(4096); d2r_test_mode = 2;
        invoke(kind, (uintptr_t)(guard + page - 16));
        d2r_trace_header h; uint8_t *data = finish(&h);
        d2r_trace_record *r = (void *)(data + 256);
        assert(h.empty_pops == 1 && !h.errors && r->flags == D2R_RECORD_EMPTY);
        assert(!r->payload_length && !r->original_length);
        free(data);
    }
    assert(!munmap(guard, page * 2));
}
static void bad_spans(void) {
    for (unsigned scenario = 0; scenario < 5; scenario++) {
        start(4096);
        uint8_t payload[8] = {0}; uintptr_t span[2] = {(uintptr_t)payload, (uintptr_t)payload + 8};
        if (scenario == 0) { span[0] = 20; span[1] = 10; }
        if (scenario == 1) { span[0] = 1; span[1] = 2; }
        if (scenario == 2) { span[0] = 1; span[1] = 1 + D2R_TRACE_OBSERVATION_CAP + 1; }
        uintptr_t descriptor = scenario == 3 ? 1 : (uintptr_t)span;
        if (scenario == 4) {
            d2r_test_mode = 1; d2r_test_span[0] = (uintptr_t)payload; d2r_test_span[1] = (uintptr_t)payload + 3;
            uint8_t result[24]; invoke(4, (uintptr_t)result);
        } else invoke(2, descriptor);
        d2r_trace_header h; uint8_t *data = finish(&h);
        d2r_trace_record *r = (void *)(data + 256);
        assert(h.errors == 1 && h.flags & D2R_TRACE_CAPTURE_GAP && r->payload_length == 0);
        assert(r->flags & (scenario == 2 ? D2R_RECORD_CAP_EXCEEDED : D2R_RECORD_BAD_SPAN));
        free(data);
    }
}
static void *other_thread(void *unused) { (void)unused; invoke(2, 1); return NULL; }
static void thread_and_overflow(void) {
    start(4096); pthread_t thread;
    assert(!pthread_create(&thread, NULL, other_thread, NULL)); assert(!pthread_join(thread, NULL));
    d2r_trace_header h; uint8_t *data = finish(&h);
    assert(h.thread_violations == 1 && h.errors == 1 && !h.records && h.attempts == 1 && h.committed == 256);
    free(data);
    start(256); uint32_t mode = 0; memcpy(client + 0x14, &mode, 4); invoke(2, 1);
    data = finish(&h); assert(h.overflow == 1 && h.errors == 1 && !h.records && d2r_test_calls == 1); free(data);
}
static void profile_rejections(void) {
    assert(d2r_probe_test_profile(NULL, 0) == -1);
    d2r_probe_module modules[2] = {{"main", (void *)1, 1}, {"sdk", (void *)1, 1}};
    assert(d2r_probe_test_profile(modules, 2) == -1);
    modules[0].size = D2R_MAIN_SIZE; modules[0].base = (void *)(UINTPTR_MAX - 16);
    modules[1].size = D2R_SDK_SIZE;
    assert(d2r_probe_test_profile(modules, 2) == -1);
    modules[1].name = "main"; assert(d2r_probe_test_profile(modules, 2) == -1);
    assert(d2r_probe_install(NULL, 0, NULL, NULL, NULL, NULL, frame) == -1);
    assert(d2r_probe_validate_stop(NULL, "trace") == -1);
    assert(d2r_probe_validate_stop("relative-stop", "trace") == -1);
    assert(d2r_probe_validate_stop(path, path) == -1);
}
static void hashes_and_tables(void) {
    char file[4096]; snprintf(file, sizeof file, "%s/synthetic-module", directory);
    int fd = open(file, O_CREAT | O_EXCL | O_WRONLY, 0600); assert(fd >= 0);
    assert(write(fd, "abc", 3) == 3 && !close(fd));
    const uint8_t sha[32] = {0xba,0x78,0x16,0xbf,0x8f,0x01,0xcf,0xea,0x41,0x41,0x40,0xde,0x5d,0xae,0x22,0x23,
                             0xb0,0x03,0x61,0xa3,0x96,0x17,0x7a,0x9c,0xb4,0x10,0xff,0x61,0xf2,0x00,0x15,0xad};
    assert(!d2r_probe_test_digest(directory, "synthetic-module", 3, sha));
    assert(d2r_probe_test_digest(directory, "synthetic-module", 2, sha) == -1);
    uint8_t wrong[32] = {0}; assert(d2r_probe_test_digest(directory, "synthetic-module", 3, wrong) == -1);
    char link[4096]; snprintf(link, sizeof link, "%s/module-link", directory);
    assert(!symlink("synthetic-module", link));
    assert(d2r_probe_test_digest(directory, "module-link", 3, sha) == -1);
    uintptr_t server_source[16], client_source[28], server_result[16], client_result[28];
    for (unsigned i = 0; i < 16; i++) server_source[i] = 0x5000 + i;
    for (unsigned i = 0; i < 28; i++) client_source[i] = 0x6000 + i;
    d2r_probe_test_clones(server_source, client_source, server_result, client_result);
    for (unsigned i = 0; i < 16; i++) {
        uintptr_t expected = server_source[i];
        if (i == 7) expected = (uintptr_t)d2r_probe_server_send;
        if (i == 12) expected = (uintptr_t)d2r_probe_system_pop;
        if (i == 13) expected = (uintptr_t)d2r_probe_gameplay_pop;
        assert(server_result[i] == expected);
    }
    for (unsigned i = 0; i < 28; i++) {
        uintptr_t expected = client_source[i];
        if (i == 2) expected = (uintptr_t)d2r_probe_client_high_pop;
        if (i == 3) expected = (uintptr_t)d2r_probe_client_low_pop;
        if (i == 8) expected = (uintptr_t)d2r_probe_client_send;
        assert(client_result[i] == expected);
    }
}
static void public_capacity_fixture(void) {
    start(D2R_TRACE_CAPACITY);
    uint8_t opaque[] = {0x6f, 0x01};
    uintptr_t span[] = {(uintptr_t)opaque, (uintptr_t)opaque + sizeof opaque};
    invoke(1, (uintptr_t)span);
    uint8_t popped[] = {0, 0, 0, 0, 0x67};
    uint8_t result[24]; d2r_test_mode = 1;
    d2r_test_span[0] = (uintptr_t)popped; d2r_test_span[1] = (uintptr_t)popped + sizeof popped;
    invoke(3, (uintptr_t)result);
    d2r_trace_header h; uint8_t *data = finish(&h);
    assert(h.records == 2 && h.attempts == 1 && h.pops == 1 && !h.errors);
    assert(h.flags == D2R_TRACE_CLOSED);
    free(data);
    printf("PASS public-capacity synthetic trace (profile not qualified): %s\n", path);
}
static void wait_phase(unsigned expected) {
    for (unsigned i = 0; i < 2000; i++) {
        if (d2r_probe_test_phase() == expected) return;
        usleep(1000);
    }
    assert(!"capture phase timeout");
}
static void read_header(d2r_trace_header *header) {
    int fd = open(path, O_RDONLY); assert(fd >= 0);
    assert(pread(fd, header, sizeof *header, 0) == sizeof *header);
    assert(!close(fd));
}
static void *request_stop(void *unused) {
    (void)unused;
    pthread_mutex_lock(&block_lock);
    while (!block_entered) pthread_cond_wait(&block_changed, &block_lock);
    pthread_mutex_unlock(&block_lock);
    int fd = open(stop_path, O_WRONLY | O_CREAT | O_EXCL, 0600);
    assert(fd >= 0 && !close(fd));
    wait_phase(1);
    d2r_trace_header header; read_header(&header);
    assert(!(header.flags & D2R_TRACE_CLOSED) && header.committed == 256 && header.pops == 1);
    pthread_mutex_lock(&block_lock); block_release = 1;
    pthread_cond_broadcast(&block_changed); pthread_mutex_unlock(&block_lock);
    return NULL;
}
static void explicit_barrier(unsigned kind) {
    start(4096); block_entered = block_release = 0;
    snprintf(stop_path, sizeof stop_path, "%s/stop-%u", directory, test_number);
    assert(!d2r_probe_test_control(stop_path));
    uint8_t payload[] = {0x01, 0, 0, 0, 0x67}; uint8_t result[24];
    d2r_test_span[0] = (uintptr_t)payload; d2r_test_span[1] = (uintptr_t)payload + sizeof payload;
    d2r_test_mode = 3;
    pthread_t stopper; assert(!pthread_create(&stopper, NULL, request_stop, NULL));
    invoke(kind, (uintptr_t)result);
    assert(!pthread_join(stopper, NULL)); wait_phase(2);
    d2r_trace_header before, after; read_header(&before);
    assert(before.flags == D2R_TRACE_CLOSED && before.records == 1 && before.pops == 1);
    d2r_test_mode = 0;
    invoke(2, 1);
    pthread_t late; assert(!pthread_create(&late, NULL, other_thread, NULL)); assert(!pthread_join(late, NULL));
    read_header(&after); assert(!memcmp(&before, &after, sizeof before));
    assert(d2r_test_calls == 3);
    memset(payload, 0, sizeof payload);
    d2r_trace_header final; uint8_t *data = finish(&final);
    assert(!memcmp(data + 320, (uint8_t[]){0x01, 0, 0, 0, 0x67}, 5));
    free(data);
}
static void abrupt_exit_is_unclosed(void) {
    char child_path[4096]; snprintf(child_path, sizeof child_path, "%s/case-%u.trace", directory, test_number);
    pid_t child = fork(); assert(child >= 0);
    if (!child) {
        start(4096); memset(client + 0x14, 0, 4); invoke(2, 1); _exit(0);
    }
    int status; assert(waitpid(child, &status, 0) == child && WIFEXITED(status) && WEXITSTATUS(status) == 0);
    test_number++;
    int fd = open(child_path, O_RDONLY); assert(fd >= 0);
    d2r_trace_header header; assert(read(fd, &header, sizeof header) == sizeof header && !close(fd));
    assert(!(header.flags & D2R_TRACE_CLOSED) && header.records == 1 && header.committed == 320);
}
int main(int argc, char **argv) {
    setvbuf(stdout, NULL, _IONBF, 0);
    assert(argc == 2);
    snprintf(directory, sizeof directory, "%s/synthetic-XXXXXX", argv[1]);
    assert(mkdtemp(directory));
    sends(); puts("PASS register/vector/NZCV/once preservation and owned send copies");
    ignored_and_gaps(); puts("PASS ignored descriptor guards and explicit unsupported mode gap");
    pops(); puts("PASS original x8 destination, owned pop copies and inaccessible empty pointers");
    bad_spans(); puts("PASS descriptor/span/bounds/observation-cap failures");
    thread_and_overflow(); puts("PASS thread violation and capacity failure still forward once");
    profile_rejections(); puts("PASS profile argument and module-bounds rejections");
    hashes_and_tables(); puts("PASS synthetic hash guards and full negative-header/14-slot/26-slot clones");
    uintptr_t targets[D2R_TRACE_KIND_COUNT] = {1, 1, 1, 1, 1, 1};
    errno = 0; assert(d2r_probe_test_start(path, 4096, server, client, targets, frame) == -1 && errno == EEXIST);
    puts("PASS exclusive trace creation preserves existing output");
    for (unsigned kind = 3; kind <= D2R_TRACE_KIND_COUNT; kind++) explicit_barrier(kind);
    puts("PASS explicit stop waits through every server/client original/postpop and later calls only forward");
    abrupt_exit_is_unclosed(); puts("PASS abrupt _exit remains an unclosed prefix");
    public_capacity_fixture();
    return 0;
}
