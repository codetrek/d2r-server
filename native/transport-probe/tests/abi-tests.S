#include "abi.h"
.text
.p2align 2
.globl _d2r_test_call
_d2r_test_call:
    stp x19, x20, [sp, #-32]!
    stp x29, x30, [sp, #16]
    mov x29, sp
    mov x19, x0
    mov x20, x1
    ldp q0, q1, [x20, #80]
    ldp q2, q3, [x20, #112]
    ldp q4, q5, [x20, #144]
    ldp q6, q7, [x20, #176]
    ldr x16, [x20, #72]
    msr nzcv, x16
    ldp x0, x1, [x20, #0]
    ldp x2, x3, [x20, #16]
    ldp x4, x5, [x20, #32]
    ldp x6, x7, [x20, #48]
    ldr x8, [x20, #64]
    blr x19
    stp x0, x1, [x20, #0]
    stp x2, x3, [x20, #16]
    stp x4, x5, [x20, #32]
    stp x6, x7, [x20, #48]
    str x8, [x20, #64]
    mrs x16, nzcv
    str x16, [x20, #72]
    stp q0, q1, [x20, #80]
    stp q2, q3, [x20, #112]
    stp q4, q5, [x20, #144]
    stp q6, q7, [x20, #176]
    ldp x29, x30, [sp, #16]
    ldp x19, x20, [sp], #32
    ret

.globl _d2r_test_original
_d2r_test_original:
    adrp x16, _d2r_test_seen@PAGE
    add x16, x16, _d2r_test_seen@PAGEOFF
    stp x0, x1, [x16, #0]
    stp x2, x3, [x16, #16]
    stp x4, x5, [x16, #32]
    stp x6, x7, [x16, #48]
    str x8, [x16, #64]
    mrs x17, nzcv
    str x17, [x16, #72]
    stp q0, q1, [x16, #80]
    stp q2, q3, [x16, #112]
    stp q4, q5, [x16, #144]
    stp q6, q7, [x16, #176]
    adrp x16, _d2r_test_calls@PAGE
    ldr x17, [x16, _d2r_test_calls@PAGEOFF]
    add x17, x17, #1
    str x17, [x16, _d2r_test_calls@PAGEOFF]
    adrp x16, _d2r_test_mode@PAGE
    ldr w16, [x16, _d2r_test_mode@PAGEOFF]
    cmp w16, #3
    b.ne Lnot_blocking
    stp x8, x30, [sp, #-16]!
    bl _d2r_test_block_until_release
    ldp x8, x30, [sp], #16
    b Lvalid
Lnot_blocking:
    cmp w16, #2
    b.eq Lempty
    cmp w16, #1
    b.ne Lreturns
Lvalid:
    adrp x16, _d2r_test_span@PAGE
    add x16, x16, _d2r_test_span@PAGEOFF
    ldp x16, x17, [x16]
    stp x16, x17, [x8]
    mov w16, #1
    strb w16, [x8, #16]
    b Lreturns
Lempty:
    strb wzr, [x8, #16]
Lreturns:
    mov x0, #0x101
    mov x1, #0x202
    mov x2, #0x303
    mov x3, #0x404
    mov x4, #0x505
    mov x5, #0x606
    mov x6, #0x707
    mov x7, #0x808
    mov x8, #0x909
    movi v0.16b, #0xa0
    movi v1.16b, #0xa1
    movi v2.16b, #0xa2
    movi v3.16b, #0xa3
    movi v4.16b, #0xa4
    movi v5.16b, #0xa5
    movi v6.16b, #0xa6
    movi v7.16b, #0xa7
    mov x16, #0xa0000000
    msr nzcv, x16
    ret
