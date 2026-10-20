# Repository instructions

## Scope and ownership

- This repository owns `d2r-server`: Linux adaptation, room workers, gateway,
  persistence, deployment, and server qualification tools.
- The `d2r-mobile` fork owns phone UI, client transport integration, phone save
  exchange, and necessary shared portability interfaces.
- Keep one source of truth for shared code. Do not copy the complete mobile host
  here, silently patch downloaded sources, or introduce a third shared repository
  without a concrete dependency design. The Mac fixture tool archives a pinned
  upstream revision into ignored private output; this is not a vendored host.
- `upstream.lock.json` is static provenance and a bounded qualification summary.
  Programs do not use it as runtime configuration. It is not an installed SDK,
  qualified client version, or completed server integration.

## Product requirements and evidence

- Target four modified iPhone clients per room, fresh phone-local characters on
  each admission, and Linux/Docker deployment. The game worker CPU is undecided.
- G0–G5 remain unpassed. A fixed macOS ARM64 full-client single-player baseline
  has entered a world, advanced 159 ticks, saved through the normal exit path,
  and returned to the menu. A separately built native observer later recorded a
  213-tick local movement run: all 62 system and 26 gameplay inputs matched
  their per-channel postpop bytes in order. This remains bounded single-process
  local observation, not cross-channel global ordering. It does
  not qualify Linux, a room worker, two-process transport, or multiplayer.
- Preserve the exact fixture, host, shader-chain, and run receipt identities.
  Source inspection, reconstructed bytes, Go cross-compilation, synthetic fixture
  tests, and container checks cannot substitute for game runtime qualification.
- Keep readiness and capabilities truthful. The production worker remains
  unavailable and readiness remains 503 until its own qualification passes.
- Preserve room-wide checkpoint consistency, pre-attachment abort fencing,
  phone account leases, and idempotent result recovery when implementing them.
  Normal single-player save commits do not prove crash or power-loss durability.
- Distinguish genuine REA evidence, shell disassembly, synthetic tests, fixture
  preparation receipts, and actual controlled game execution.

## Development

- Use Go 1.26 or later and the standard library unless a dependency has a
  demonstrated purpose. Use `make` so caches and temporary files stay in `.tmp`.
- Run `make check` and checks relevant to changed platform code. `make check`
  runs command tests separately from core coverage, without duplicating tests.
  `make build` and cross-build include the compact `d2r-trace` reader.
- Python fixture tooling lives in `tools/mac-fixture/` and uses uv with its
  committed lockfile. Run `make fixture-test` for locked Python 3.12 synthetic
  tests. Keep uv, Python installations, environments, and caches under `.tmp`.
  The probe builder only prepares an independent `built_unexecuted` variant.
  `make native-probe-test` requires Darwin ARM64 and uses synthetic originals;
  run the relevant native/sanitizer checks when changing observer code.
- The fixture preparation command builds a private candidate and never executes
  the game or installs system tools. Actual execution requires a separate owned
  experiment with explicit input, network, write, lifecycle, and cleanup bounds.
- Select `main` or `sdk` explicitly in the Go inspector API. The CLI defaults to
  `main`; manifests identify their module and layout profile with schema 2.
- Keep Docker and GitHub Actions dependencies pinned. CI uses synthetic inputs;
  fixture CI does not need game assets, Apple GPU execution, or player saves.
  Workflow configuration is not evidence that a branch run passed.
- Record primary-source links for non-obvious platform and function-address
  assumptions. Preserve TLS/SVC wrappers, guest thread context, and buffer
  lifetimes; analysis-body offsets are not standalone ABI call entries.
- Native traces describe attempts and postpop observations, never proven network
  deliveries. Require explicit `D2R_PROBE_STOP_PATH` capture admission closure
  and draining; the host uses `_exit`, so atexit cannot close the capture. Keep
  incomplete/error/gap/overflow/foreign-thread captures ineligible. Closure covers
  only the declared interval. Raw traces stay private; reports omit payloads.
- Real game files, saves, settings, player names, screenshots, raw pseudocode,
  REA sessions, local review logs, and portable tools remain ignored in `.tmp`.
- Write user-facing documentation in Chinese. Rewrite affected documents
  coherently and synchronize all relevant docs and provenance summaries.
- Follow the user's Git authorization rules; never add AI co-author trailers.

## Local research layout

- `.tmp/inputs/`: privately supplied signed Guest and matching metadata.
- `.tmp/rea/`: historical REA evidence and analysis-only derived images.
- `.tmp/rea-provider-recovery/`: source anchors and provider recovery records.
- `.tmp/tools/`: portable analysis and build tools with publisher provenance.
- `.tmp/migration/manifest.json`: verified local migration inventory.
- `.tmp/reviews/`: private review findings and historical design reviews.
- `.tmp/mac-fixture/`: prepared private candidates, tool receipts, and metadata
  identity checks. Prepared output is not an execution receipt.
- `.tmp/experiments/`: owned static and runtime experiments with exact identities,
  scoped logs, expected/actual assertions, and cleanup records.

Historical evidence embeds original paths. Preserve it unchanged and resolve
current copies through the migration inventory. Public docs expose only bounded
facts, identities, and verification scope, never proprietary payloads or paths.
