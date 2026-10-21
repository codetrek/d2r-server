#include "lifecycle.h"
#include <stdatomic.h>

typedef void (*gate_callback)(const uint64_t *);
static _Atomic(gate_callback) primary[D2R_LIFE_GATE_COUNT];

static void dispatch(unsigned id, const uint64_t *registers) {
    gate_callback callback = atomic_load_explicit(&primary[id], memory_order_acquire);
    /* A dispatcher already loaded by its caller can outlive unregistration. */
    if (!callback) return;
    unsigned token = d2r_lifecycle_enter(id, registers);
    callback(registers);
    d2r_lifecycle_leave(token, id, registers);
}
static void ui_dispatch(const uint64_t *registers) { dispatch(D2R_LIFE_GATE_UI, registers); }
static void tick_dispatch(const uint64_t *registers) { dispatch(D2R_LIFE_GATE_TICK, registers); }

void gate_obs_set(int id, gate_callback callback) {
    if (id != D2R_LIFE_GATE_UI && id != D2R_LIFE_GATE_TICK) {
        d2r_upstream_gate_obs_set(id, callback);
        return;
    }
    /* The upstream registry replaces a callback, rather than adding a listener.
     * Keep that last-registration contract when diagnostics replace product hooks.
     * https://github.com/jhaizhou-ops/d2r-mobile/blob/120fe55c02a5633476998a44f8c3c3c809fa48b0/dm/host/hle.c#L64 */
    atomic_store_explicit(&primary[id], callback, memory_order_release);
    d2r_upstream_gate_obs_set(id, callback ? (id == D2R_LIFE_GATE_UI ? ui_dispatch : tick_dispatch) : NULL);
}
