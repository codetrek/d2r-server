#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
case $(uname -s):$(uname -m) in Darwin:arm64) ;; *) printf '%s\n' 'Native probe tests require Darwin ARM64.' >&2; exit 2;; esac
out="$root/.tmp/native-probe"
mkdir -p "$out"
clang -arch arm64 -std=c11 -O1 -g -Wall -Wextra -Werror -DD2R_PROBE_TESTING \
  -I"$root/native/transport-probe" \
  "$root/native/transport-probe/observer.c" "$root/native/transport-probe/profile.c" \
  "$root/native/transport-probe/lifecycle.c" "$root/native/transport-probe/gate.c" \
  "$root/native/transport-probe/tests/registry.c" \
  "$root/native/transport-probe/shims.S" "$root/native/transport-probe/tests/test.c" \
  "$root/native/transport-probe/tests/abi-tests.S" -o "$out/synthetic-tests"
"$out/synthetic-tests" "$out"
clang -arch arm64 -std=c11 -O1 -g -Wall -Wextra -Werror -DD2R_PROBE_TESTING \
  -I"$root/native/transport-probe" \
  "$root/native/transport-probe/observer.c" "$root/native/transport-probe/profile.c" \
  "$root/native/transport-probe/lifecycle.c" "$root/native/transport-probe/gate.c" \
  "$root/native/transport-probe/shims.S" "$root/native/transport-probe/tests/registry.c" \
  "$root/native/transport-probe/tests/lifecycle.c" -o "$out/lifecycle-tests"
"$out/lifecycle-tests" "$out"
