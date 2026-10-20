# Repository instructions

## Scope and ownership

- This repository owns `d2r-server`: Linux adaptation, room workers, gateway,
  persistence, deployment, and server qualification tools.
- The `d2r-mobile` fork owns phone UI, client transport integration, phone save
  exchange, and necessary shared portability interfaces.
- Keep one source of truth for shared code. Do not copy the complete mobile host
  here, silently patch downloaded sources, or introduce a third shared repository
  without a concrete dependency design.
- `upstream.lock.json` is a provenance record. It is not an installed SDK,
  qualified client version, or completed runtime integration.

## Product requirements and evidence

- Target four modified iPhone clients per room, fresh phone-local characters on
  each admission, and Linux/Docker deployment. The game worker CPU is undecided.
- G0–G5 in the design document are unpassed. Source inspection, reconstructed
  bytes, Go cross-compilation, and container tests do not prove a working game
  server or multiplayer.
- Keep readiness and capabilities truthful. Never report a playable room or a
  qualified engine without the corresponding runtime evidence.
- Preserve room-wide checkpoint consistency, pre-attachment abort fencing,
  phone account leases, and idempotent result recovery when implementing them.
- Distinguish genuine REA evidence from shell disassembly and synthetic inputs.

## Development

- Use Go 1.26 or later and the standard library unless a dependency has a
  demonstrated purpose. Use `make` so caches and temporary files stay in `.tmp`.
- Run `make check` and checks relevant to changed platform code. `make check`
  runs command tests separately from core coverage, without duplicating tests.
- Keep Docker and GitHub Actions dependencies pinned. Record research links for
  non-obvious platform decisions and original function-address assumptions.
- Tests use synthetic data. Real game files, saves, raw pseudocode, REA sessions,
  local review logs, and portable analysis tools stay ignored under `.tmp`.
- Write user-facing documentation in Chinese. Rewrite affected documents
  coherently and keep all relevant documentation synchronized.
- Follow the user's Git authorization rules; never add AI co-author trailers.

## Local research layout

- `.tmp/inputs/`: privately supplied signed Guest and matching fixups.
- `.tmp/rea/`: historical REA evidence and analysis-only derived images.
- `.tmp/rea-provider-recovery/`: source anchors and provider recovery records.
- `.tmp/tools/`: portable Ghidra/JDK, with publisher provenance retained.
- `.tmp/migration/manifest.json`: verified local migration inventory.
- `.tmp/reviews/`: private review findings and historical design reviews.

Historical evidence embeds original paths. Preserve those records unchanged;
use the migration inventory to resolve their current copies.
