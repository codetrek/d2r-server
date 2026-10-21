#include "internal.h"
#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

extern void server_shim(void *, uint32_t, const uintptr_t *) __asm__("_d2r_probe_server_send");
extern void client_shim(void *, const uintptr_t *) __asm__("_d2r_probe_client_send");
static char directory[4096], trace_path[4096], report_path[4096];
static const char *relay_binary;
static unsigned number, server_calls, client_calls, nesting;
static uint8_t server[64], client[0x300], small[] = {0xaf, 0x13, 0x37}, command[] = {0x68, 0, 0x80, 0xfe, 1};
static uint8_t *large;
static uintptr_t small_span[2], command_span[2], empty_span[2], large_span[2];
static pthread_t owner;
static volatile uint32_t *forbidden_original;

static uint64_t frame(void) { assert(pthread_equal(owner, pthread_self())); return 321; }
static void original_client(void *object, const uintptr_t *span) {
    if (forbidden_original) *forbidden_original = 1;
    assert(object == client && span != command_span && span[0] != (uintptr_t)command);
    assert(span[1] - span[0] == sizeof command && !memcmp((void *)span[0], command, sizeof command));
    client_calls++; errno = ERANGE;
}
static void original_server(void *object, uint32_t cid, const uintptr_t *span) {
    assert(object == server && !cid);
    size_t length = span[1] - span[0];
    if (!length) assert(span == empty_span);
    else if (length == sizeof small) {
        assert(span != small_span && span[0] != (uintptr_t)small && !memcmp((void *)span[0], small, length));
        if (nesting) {
            nesting = 0; client_shim(client, command_span);
            assert(!memcmp((void *)span[0], small, length));
        }
    } else {
        assert(length == D2R_PIPE_MAX_PAYLOAD && span != large_span && span[0] != (uintptr_t)large);
        assert(!memcmp((void *)span[0], large, length));
    }
    server_calls++; errno = ERANGE;
}
static void start_probe(void) {
    memset(server, 0, sizeof server); memset(client, 0, sizeof client);
    uint32_t mode = 1; memcpy(client + 0x14, &mode, 4); owner = pthread_self();
    snprintf(trace_path, sizeof trace_path, "%s/trace-%u.bin", directory, number++);
    uintptr_t targets[D2R_TRACE_KIND_COUNT] = {(uintptr_t)original_server, (uintptr_t)original_client, 1, 1, 1, 1};
    assert(!d2r_probe_test_start(trace_path, D2R_TRACE_CAPACITY, server, client, targets, frame));
}
static pid_t start_child(int fake, int *request, int *response) {
    int incoming[2], outgoing[2]; assert(!pipe(incoming) && !pipe(outgoing));
    snprintf(report_path, sizeof report_path, "%s/report-%u.json", directory, number++);
    pid_t child = fork(); assert(child >= 0);
    if (!child) {
        assert(dup2(incoming[0], 0) == 0 && dup2(outgoing[1], 1) == 1);
        assert(!close(incoming[0]) && !close(incoming[1]) && !close(outgoing[0]) && !close(outgoing[1]));
        if (!fake) {
            execl(relay_binary, relay_binary, "-report", report_path, (char *)NULL); _exit(127);
        }
        signal(SIGPIPE, SIG_IGN);
        if (fake == 1) _exit(0);
        d2r_pipe_header header;
        size_t at = 0;
        while (at < sizeof header) { ssize_t got = read(0, (uint8_t *)&header + at, sizeof header - at); if (got <= 0) _exit(80); at += (size_t)got; }
        assert(!memcmp(header.magic, D2R_PIPE_MAGIC, 8));
        if (fake == 5 || fake == 6) {
            assert(header.type == D2R_PIPE_CLOSE);
            uint8_t extra; assert(read(0, &extra, 1) == 0);
            header.type = D2R_PIPE_ACK;
            if (fake == 5) header.sequence++;
            assert(write(1, &header, sizeof header) == sizeof header);
            if (fake == 6) assert(write(1, "x", 1) == 1);
            _exit(0);
        }
        uint8_t payload[16]; assert(header.payload_length <= sizeof payload);
        at = 0;
        while (at < header.payload_length) { ssize_t got = read(0, payload + at, header.payload_length - at); if (got <= 0) _exit(81); at += (size_t)got; }
        assert(header.type == D2R_PIPE_S2C && header.sequence == 1);
        if (fake == 2) header.magic[0] ^= 1;
        if (fake == 4) {
            for (size_t i = 0; i < sizeof header; i++) {
                if (write(1, (uint8_t *)&header + i, 1) != 1) _exit(0);
                usleep(250000);
            }
        } else assert(write(1, &header, sizeof header) == sizeof header);
        if (fake == 3) payload[0] ^= 1;
        if (header.payload_length) (void)write(1, payload, header.payload_length);
        _exit(0);
    }
    assert(!close(incoming[0]) && !close(outgoing[1]));
    *request = incoming[1]; *response = outgoing[0]; return child;
}
static void configure(int request, int response) {
    char read_text[32], write_text[32];
    snprintf(read_text, sizeof read_text, "%d", response); snprintf(write_text, sizeof write_text, "%d", request);
    assert(!d2r_pipe_configure("relay", read_text, write_text));
    assert(fcntl(request, F_GETNOSIGPIPE) == 1);
}
static void reap(pid_t child, int success) {
    int status; assert(waitpid(child, &status, 0) == child);
    if (success) assert(WIFEXITED(status) && WEXITSTATUS(status) == 0);
}
static void positive(void) {
    int request, response; pid_t child = start_child(0, &request, &response);
    configure(request, response); start_probe();
    nesting = 1; server_calls = client_calls = 0;
    server_shim(server, 0, small_span); assert(errno == ERANGE);
    server_shim(server, 0, empty_span); assert(errno == ERANGE);
    server_shim(server, 0, large_span); assert(errno == ERANGE);
    assert(server_calls == 3 && client_calls == 1);
    d2r_probe_close(); reap(child, 1);
    int fd = open(trace_path, O_RDONLY); assert(fd >= 0);
    d2r_trace_header header; assert(read(fd, &header, sizeof header) == sizeof header && !close(fd));
    assert(header.records == 4 && header.attempts == 4 && !header.errors && header.flags == D2R_TRACE_CLOSED);
    fd = open(report_path, O_RDONLY); assert(fd >= 0);
    struct stat info; assert(!fstat(fd, &info) && (info.st_mode & 0777) == 0600);
    char json[4096]; ssize_t n = read(fd, json, sizeof json - 1); assert(n > 0 && !close(fd)); json[n] = 0;
    assert(strstr(json, "\"input_complete\":true") && strstr(json, "\"data_frames\":4") && strstr(json, "\"close_sequence\":5"));
    d2r_probe_test_reset();
}
static void abort_expected(int signal_number) { (void)signal_number; _exit(99); }
static void empty_c2s_never_reaches_original(void) {
    forbidden_original = mmap(NULL, 4096, PROT_READ | PROT_WRITE, MAP_ANON | MAP_SHARED, -1, 0);
    assert(forbidden_original != MAP_FAILED); *forbidden_original = 0;
    pid_t child = fork(); assert(child >= 0);
    if (!child) {
        signal(SIGABRT, abort_expected);
        int req[2], rep[2]; assert(!pipe(req) && !pipe(rep)); configure(req[1], rep[0]); start_probe();
        client_shim(client, empty_span); _exit(98);
    }
    int status; assert(waitpid(child, &status, 0) == child && WIFEXITED(status) && WEXITSTATUS(status) == 99);
    assert(!*forbidden_original && !munmap((void *)forbidden_original, 4096)); forbidden_original = NULL;
}
static void faulted_reply(unsigned fault, int expected) {
    int request, response; pid_t child = start_child((int)fault, &request, &response);
    if (fault == 1) reap(child, 1);
    struct sigaction before, after; assert(!sigaction(SIGPIPE, NULL, &before));
    configure(request, response);
    assert(!sigaction(SIGPIPE, NULL, &after) && before.sa_handler == after.sa_handler && before.sa_flags == after.sa_flags);
    struct { d2r_trace_record record; uint8_t payload[8]; } capture = {0};
    capture.record.kind = D2R_TRACE_SERVER_SEND_ATTEMPT; capture.record.flags = D2R_RECORD_PAYLOAD | D2R_RECORD_CLIENT_ID;
    capture.record.payload_length = sizeof small; memcpy(capture.payload, small, sizeof small);
    d2r_probe_abi state = {0};
    uint64_t started = clock_gettime_nsec_np(CLOCK_MONOTONIC);
    assert(d2r_pipe_prepare(&state, &capture.record) == -1 && errno == expected && !state.relay_call);
    if (fault == 4) {
        assert(clock_gettime_nsec_np(CLOCK_MONOTONIC) - started < UINT64_C(5000000000));
        assert(!kill(child, SIGTERM));
    }
    d2r_pipe_test_reset(); if (fault != 1) reap(child, fault != 4);
}
static void terminal_failure(unsigned fault) {
    int request, response; pid_t child = start_child((int)fault, &request, &response);
    configure(request, response); start_probe(); d2r_probe_close(); reap(child, 1);
    int fd = open(trace_path, O_RDONLY); assert(fd >= 0);
    d2r_trace_header header; assert(read(fd, &header, sizeof header) == sizeof header && !close(fd));
    assert(header.errors == 1 && header.flags == D2R_TRACE_CAPTURE_GAP);
    d2r_probe_test_reset();
}
static void configuration_rejections(void) {
    assert(d2r_pipe_configure(NULL, NULL, NULL) == -1 && errno == EINVAL);
    assert(d2r_pipe_configure("unknown", NULL, NULL) == -1 && errno == EINVAL);
    assert(d2r_pipe_configure("observe", "3", NULL) == -1 && errno == EINVAL);
    for (unsigned i = 0; i < 4; i++) {
        const char *text[] = {"", "2", "-1", "3x"};
        assert(d2r_pipe_configure("relay", text[i], "4") == -1 && errno == EINVAL);
    }
    char path[4096], fd_text[32]; snprintf(path, sizeof path, "%s/not-a-pipe", directory);
    int fd = open(path, O_CREAT | O_EXCL | O_RDONLY, 0600); assert(fd >= 0);
    snprintf(fd_text, sizeof fd_text, "%d", fd);
    assert(d2r_pipe_configure("relay", fd_text, "4") == -1 && errno == EINVAL && !close(fd));
    assert(!d2r_pipe_configure("observe", NULL, NULL));
    assert(d2r_pipe_configure("observe", NULL, NULL) == -1 && errno == EALREADY);
    d2r_pipe_test_reset();
}
int main(int argc, char **argv) {
    setvbuf(stdout, NULL, _IONBF, 0); assert(argc == 3); relay_binary = argv[2];
    snprintf(directory, sizeof directory, "%s/relay-XXXXXX", argv[1]); assert(mkdtemp(directory));
    large = malloc(D2R_PIPE_MAX_PAYLOAD); assert(large);
    for (size_t i = 0; i < D2R_PIPE_MAX_PAYLOAD; i++) large[i] = (uint8_t)i;
    small_span[0] = (uintptr_t)small; small_span[1] = small_span[0] + sizeof small;
    command_span[0] = (uintptr_t)command; command_span[1] = command_span[0] + sizeof command;
    large_span[0] = (uintptr_t)large; large_span[1] = large_span[0] + D2R_PIPE_MAX_PAYLOAD;
    configuration_rejections(); puts("PASS explicit mode and owned pipe descriptor configuration");
    positive(); puts("PASS actual Go child, owned reply spans, nested original calls, empty S2C and 1MiB transfer");
    empty_c2s_never_reaches_original(); puts("PASS active empty C2S aborts before original invocation");
    faulted_reply(1, EPIPE); puts("PASS broken pipe returns EPIPE without changing SIGPIPE disposition");
    faulted_reply(2, EPROTO); faulted_reply(3, EPROTO); puts("PASS corrupt header or bytes never become a prepared Guest argument");
    faulted_reply(4, ETIMEDOUT); puts("PASS one absolute deadline rejects trickled reply header");
    terminal_failure(5); terminal_failure(6);
    puts("PASS invalid terminal acknowledgment or trailing reply prevents transport CLOSED");
    free(large); return 0;
}
