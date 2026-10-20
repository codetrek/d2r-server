#include "probe.h"
#include "dm.h"
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>

extern uint64_t dm_guest_frame(void);

void d2r_observed_run_module_inits(void) {
    static int entered;
    if (entered++) { fputs("d2r probe: repeated module initialization\n", stderr); abort(); }
    run_module_inits();
    d2r_probe_module modules[MAX_MOD];
    if (g_nmod < 0 || g_nmod > MAX_MOD) { fputs("d2r probe: invalid module count\n", stderr); abort(); }
    for (int i = 0; i < g_nmod; i++)
        modules[i] = (d2r_probe_module){g_mod[i].name, g_mod[i].base, g_mod[i].size};
    if (d2r_probe_install(modules, (size_t)g_nmod, getenv("D2R_PROBE_MODULES"),
                           getenv("D2R_TRANSPORT_TRACE"), getenv("D2R_PROBE_STOP_PATH"), dm_guest_frame)) {
        fprintf(stderr, "d2r probe: profile or trace installation failed (errno %d)\n", errno);
        abort();
    }
    fputs("d2r probe: fixed-profile observer installed after module initialization\n", stderr);
}
