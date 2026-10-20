#include "abi.h"
#if !defined(__APPLE__) || !defined(__aarch64__)
#error This probe supports Darwin ARM64 only.
#endif

.text
.p2align 2
/* We retain the fixed profile's argument/result register bank across both C
 * observations, including the incoming indirect-result destination separately.
 * No stack arguments occur at these six recovered boundaries.
 * https://github.com/ARM-software/abi-aa/blob/17f2cc8ee6645c1709088f4e0cb4438ce75a14ff/aapcs64/aapcs64.rst#result-return
 * The frame stays 16-byte aligned and Darwin's reserved x18 is untouched:
 * https://developer.apple.com/documentation/xcode/writing-arm64-code-for-apple-platforms */
.globl _d2r_probe_server_send
_d2r_probe_server_send:
    mov x16, #1
    b Lprobe_entry
.globl _d2r_probe_client_send
_d2r_probe_client_send:
    mov x16, #2
    b Lprobe_entry
.globl _d2r_probe_system_pop
_d2r_probe_system_pop:
    mov x16, #3
    b Lprobe_entry
.globl _d2r_probe_gameplay_pop
_d2r_probe_gameplay_pop:
    mov x16, #4
    b Lprobe_entry
.globl _d2r_probe_client_high_pop
_d2r_probe_client_high_pop:
    mov x16, #5
    b Lprobe_entry
.globl _d2r_probe_client_low_pop
_d2r_probe_client_low_pop:
    mov x16, #6
Lprobe_entry:
    sub sp, sp, #D2R_ABI_FRAME_SIZE
    stp x29, x30, [sp, #D2R_ABI_STATE_SIZE]
    add x29, sp, #D2R_ABI_STATE_SIZE
    stp x0, x1, [sp, #0]
    stp x2, x3, [sp, #16]
    stp x4, x5, [sp, #32]
    stp x6, x7, [sp, #48]
    str x8, [sp, #D2R_ABI_X8]
    str x8, [sp, #D2R_ABI_ORIGINAL_X8]
    str x16, [sp, #D2R_ABI_KIND]
    mrs x16, nzcv
    str x16, [sp, #D2R_ABI_NZCV]
    stp q0, q1, [sp, #D2R_ABI_Q0]
    stp q2, q3, [sp, #112]
    stp q4, q5, [sp, #144]
    stp q6, q7, [sp, #176]
    mov x0, sp
    bl _d2r_probe_before
    ldp q0, q1, [sp, #D2R_ABI_Q0]
    ldp q2, q3, [sp, #112]
    ldp q4, q5, [sp, #144]
    ldp q6, q7, [sp, #176]
    ldr x16, [sp, #D2R_ABI_NZCV]
    msr nzcv, x16
    ldp x0, x1, [sp, #0]
    ldp x2, x3, [sp, #16]
    ldp x4, x5, [sp, #32]
    ldp x6, x7, [sp, #48]
    ldr x8, [sp, #D2R_ABI_X8]
    ldr x16, [sp, #D2R_ABI_TARGET]
    blr x16
    stp x0, x1, [sp, #0]
    stp x2, x3, [sp, #16]
    stp x4, x5, [sp, #32]
    stp x6, x7, [sp, #48]
    str x8, [sp, #D2R_ABI_X8]
    mrs x16, nzcv
    str x16, [sp, #D2R_ABI_NZCV]
    stp q0, q1, [sp, #D2R_ABI_Q0]
    stp q2, q3, [sp, #112]
    stp q4, q5, [sp, #144]
    stp q6, q7, [sp, #176]
    mov x0, sp
    bl _d2r_probe_after
    ldp q0, q1, [sp, #D2R_ABI_Q0]
    ldp q2, q3, [sp, #112]
    ldp q4, q5, [sp, #144]
    ldp q6, q7, [sp, #176]
    ldr x16, [sp, #D2R_ABI_NZCV]
    msr nzcv, x16
    ldp x0, x1, [sp, #0]
    ldp x2, x3, [sp, #16]
    ldp x4, x5, [sp, #32]
    ldp x6, x7, [sp, #48]
    ldr x8, [sp, #D2R_ABI_X8]
    ldp x29, x30, [sp, #D2R_ABI_STATE_SIZE]
    add sp, sp, #D2R_ABI_FRAME_SIZE
    ret
