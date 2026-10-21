#include "lifecycle.h"
#include <assert.h>

static void (*registered[D2R_LIFE_GATE_COUNT])(const uint64_t *);
void d2r_upstream_gate_obs_set(int id, void (*callback)(const uint64_t *)) {
    assert(id > 0 && (unsigned)id < D2R_LIFE_GATE_COUNT);
    registered[id] = callback;
}
void d2r_test_gate_fire(unsigned id, const uint64_t *registers) {
    assert(id > 0 && id < D2R_LIFE_GATE_COUNT && registered[id]);
    registered[id](registers);
}
void (*d2r_test_gate_registered(unsigned id))(const uint64_t *) { return registered[id]; }
