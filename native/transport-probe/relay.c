#include "relay.h"
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <poll.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

enum { OBSERVE, RELAY };
typedef struct { uintptr_t span[2]; uint8_t payload[]; } pipe_call;
static struct {
    unsigned mode, configured, closed;
    int input, output, close_error;
    uint64_t next_sequence, payload_bytes;
} pipe_context = {.mode = OBSERVE, .input = -1, .output = -1, .next_sequence = 1};

static int descriptor(const char *text, int access) {
    if (!text || !*text) { errno = EINVAL; return -1; }
    for (const char *p = text; *p; p++) if (*p < '0' || *p > '9') { errno = EINVAL; return -1; }
    errno = 0;
    char *end; long value = strtol(text, &end, 10);
    if (errno || *end || value < 3 || value > INT_MAX) { errno = EINVAL; return -1; }
    int fd = (int)value, flags = fcntl(fd, F_GETFL);
    if (flags < 0) return -1;
    struct stat info;
    if (fstat(fd, &info)) return -1;
    if (!S_ISFIFO(info.st_mode) || (flags & O_ACCMODE) != access) { errno = EINVAL; return -1; }
    return fd;
}
int d2r_pipe_configure(const char *mode, const char *read_fd, const char *write_fd) {
    if (pipe_context.configured) { errno = EALREADY; return -1; }
    if (!mode) { errno = EINVAL; return -1; }
    if (!strcmp(mode, "observe")) {
        if (read_fd || write_fd) { errno = EINVAL; return -1; }
        pipe_context.configured = 1; return 0;
    }
    if (strcmp(mode, "relay")) { errno = EINVAL; return -1; }
    int input = descriptor(read_fd, O_RDONLY);
    if (input < 0) return -1;
    int output = descriptor(write_fd, O_WRONLY);
    if (output < 0) return -1;
    if (input == output) { errno = EINVAL; return -1; }
    int in_flags = fcntl(input, F_GETFL), out_flags = fcntl(output, F_GETFL);
    if (in_flags < 0 || out_flags < 0 || fcntl(input, F_SETFL, in_flags | O_NONBLOCK) ||
        fcntl(output, F_SETFL, out_flags | O_NONBLOCK) ||
        fcntl(input, F_SETFD, FD_CLOEXEC) || fcntl(output, F_SETFD, FD_CLOEXEC)) return -1;
    /* EPIPE signals are suppressed on this owned pipe; Guest signal policy is retained.
     * https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/kern_descrip.c#L2944 */
    if (fcntl(output, F_SETNOSIGPIPE, 1)) return -1;
    pipe_context.mode = RELAY; pipe_context.configured = 1;
    pipe_context.input = input; pipe_context.output = output;
    return 0;
}
int d2r_pipe_active(void) { return pipe_context.mode == RELAY; }
static int wait_ready(int fd, short events, uint64_t deadline) {
    for (;;) {
        uint64_t now = clock_gettime_nsec_np(CLOCK_MONOTONIC);
        if (now >= deadline) { errno = ETIMEDOUT; return -1; }
        uint64_t remaining_ms = (deadline - now + 999999) / 1000000;
        struct pollfd item = {.fd = fd, .events = events};
        int result = poll(&item, 1, (int)remaining_ms);
        if (result < 0) { if (errno == EINTR) continue; return -1; }
        if (!result) continue;
        if (item.revents & POLLNVAL) { errno = EBADF; return -1; }
        if (item.revents & events) return 0;
        if (events == POLLIN && item.revents & POLLHUP) return 0;
        if (item.revents & (POLLERR | POLLHUP)) { errno = EPIPE; return -1; }
    }
}
static int transfer(int fd, void *buffer, size_t size, int writing, uint64_t deadline) {
    size_t at = 0;
    while (at < size) {
        if (clock_gettime_nsec_np(CLOCK_MONOTONIC) >= deadline) { errno = ETIMEDOUT; return -1; }
        ssize_t count = writing ? write(fd, (uint8_t *)buffer + at, size - at)
                                : read(fd, (uint8_t *)buffer + at, size - at);
        if (count > 0) { at += (size_t)count; continue; }
        if (!count) { errno = EPIPE; return -1; }
        if (errno == EINTR) continue;
        if (errno != EAGAIN && errno != EWOULDBLOCK) return -1;
        if (wait_ready(fd, writing ? POLLOUT : POLLIN, deadline)) return -1;
    }
    return 0;
}
static int expect_eof(int fd, uint64_t deadline) {
    for (;;) {
        if (clock_gettime_nsec_np(CLOCK_MONOTONIC) >= deadline) { errno = ETIMEDOUT; return -1; }
        uint8_t extra;
        ssize_t count = read(fd, &extra, 1);
        if (!count) return 0;
        if (count > 0) { errno = EPROTO; return -1; }
        if (errno == EINTR) continue;
        if (errno != EAGAIN && errno != EWOULDBLOCK) return -1;
        if (wait_ready(fd, POLLIN, deadline)) return -1;
    }
}
static d2r_pipe_header header(unsigned type, uint32_t cid, uint32_t length) {
    d2r_pipe_header value = {.version = D2R_PIPE_VERSION, .type = (uint16_t)type,
        .sequence = pipe_context.next_sequence, .client_id = cid, .payload_length = length};
    memcpy(value.magic, D2R_PIPE_MAGIC, 8); return value;
}
int d2r_pipe_prepare(d2r_probe_abi *state, const d2r_trace_record *record) {
    if (pipe_context.mode == OBSERVE) return 0;
    if (pipe_context.closed || state->relay_call || !record || !(record->flags & D2R_RECORD_PAYLOAD) ||
        (record->kind != D2R_TRACE_SERVER_SEND_ATTEMPT && record->kind != D2R_TRACE_CLIENT_SEND_ATTEMPT)) {
        errno = EPROTO; return -1;
    }
    uint32_t length = record->payload_length;
    if ((record->kind == D2R_TRACE_CLIENT_SEND_ATTEMPT && !length) ||
        length > D2R_PIPE_MAX_PAYLOAD || pipe_context.next_sequence > D2R_PIPE_MAX_FRAMES ||
        pipe_context.payload_bytes > D2R_PIPE_MAX_BYTES - length) { errno = EMSGSIZE; return -1; }
    pipe_call *call = length ? malloc(sizeof *call + length) : NULL;
    if (length && !call) return -1;
    unsigned type = record->kind == D2R_TRACE_CLIENT_SEND_ATTEMPT ? D2R_PIPE_C2S : D2R_PIPE_S2C;
    d2r_pipe_header request = header(type, record->client_id, length), reply;
    uint64_t deadline = clock_gettime_nsec_np(CLOCK_MONOTONIC) + D2R_PIPE_TIMEOUT_NS;
    const uint8_t *copied = (const uint8_t *)(record + 1);
    if (transfer(pipe_context.output, &request, sizeof request, 1, deadline) ||
        transfer(pipe_context.output, (void *)copied, length, 1, deadline) ||
        transfer(pipe_context.input, &reply, sizeof reply, 0, deadline)) goto failed;
    if (memcmp(&request, &reply, sizeof request)) { errno = EPROTO; goto failed; }
    if (length && transfer(pipe_context.input, call->payload, length, 0, deadline)) goto failed;
    if (length && memcmp(copied, call->payload, length)) { errno = EPROTO; goto failed; }
    if (clock_gettime_nsec_np(CLOCK_MONOTONIC) >= deadline) { errno = ETIMEDOUT; goto failed; }
    pipe_context.next_sequence++; pipe_context.payload_bytes += length;
    if (call) {
        call->span[0] = (uintptr_t)call->payload; call->span[1] = (uintptr_t)call->payload + length;
        state->relay_call = (uintptr_t)call;
        state->x[record->kind == D2R_TRACE_CLIENT_SEND_ATTEMPT ? 1 : 2] = (uintptr_t)call->span;
    }
    return 0;
failed: {
        int error = errno; free(call); errno = error; return -1;
    }
}
void d2r_pipe_release(d2r_probe_abi *state) {
    free((void *)(uintptr_t)state->relay_call); state->relay_call = 0;
}
static void close_descriptor(int *fd, int *error) {
    if (*fd >= 0) {
        if (close(*fd) && !*error) *error = errno;
        *fd = -1;
    }
}
int d2r_pipe_close(void) {
    if (pipe_context.mode == OBSERVE) return 0;
    if (pipe_context.closed) {
        if (pipe_context.close_error) { errno = pipe_context.close_error; return -1; }
        return 0;
    }
    d2r_pipe_header request = header(D2R_PIPE_CLOSE, 0, 0), reply;
    uint64_t deadline = clock_gettime_nsec_np(CLOCK_MONOTONIC) + D2R_PIPE_TIMEOUT_NS;
    int error = 0;
    if (transfer(pipe_context.output, &request, sizeof request, 1, deadline)) error = errno;
    close_descriptor(&pipe_context.output, &error);
    if (!error && transfer(pipe_context.input, &reply, sizeof reply, 0, deadline)) error = errno;
    request.type = D2R_PIPE_ACK;
    if (!error && memcmp(&request, &reply, sizeof reply)) error = EPROTO;
    if (!error && expect_eof(pipe_context.input, deadline)) error = errno;
    if (!error && clock_gettime_nsec_np(CLOCK_MONOTONIC) >= deadline) error = ETIMEDOUT;
    close_descriptor(&pipe_context.input, &error);
    pipe_context.closed = 1; pipe_context.close_error = error;
    if (error) { errno = error; return -1; }
    return 0;
}
#ifdef D2R_PROBE_TESTING
void d2r_pipe_test_reset(void) {
    int ignored = 0;
    close_descriptor(&pipe_context.input, &ignored); close_descriptor(&pipe_context.output, &ignored);
    pipe_context.mode = OBSERVE; pipe_context.configured = pipe_context.closed = 0;
    pipe_context.close_error = 0; pipe_context.next_sequence = 1; pipe_context.payload_bytes = 0;
}
#endif
