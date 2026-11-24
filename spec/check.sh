#!/bin/sh
# Runs TLC on every configuration in this directory.
# Usage: TLA2TOOLS=/path/to/tla2tools.jar spec/check.sh [config ...]
# Configurations whose name starts with "expect-" must end with a violation.
set -u
cd "$(dirname "$0")" || exit 1
: "${TLA2TOOLS:?set TLA2TOOLS to the path of tla2tools.jar}"
out="${TLC_OUT:-${TMPDIR:-/tmp}/pgcheckout-tlc}"
mkdir -p "$out"
[ $# -gt 0 ] || set -- *.cfg
status=0
for cfg in "$@"; do
  name="${cfg%.cfg}"
  rm -rf "$out/$name"
  java -XX:+UseParallelGC -cp "$TLA2TOOLS" tlc2.TLC -workers auto \
    -metadir "$out/$name" -config "$name.cfg" pgcheckout.tla > "$out/$name.log" 2>&1
  summary=$(grep -E "distinct states found, 0 states left" "$out/$name.log" | tail -1)
  violated=$(grep -E "^Error: (Invariant|Action property) .* (is|was) violated" "$out/$name.log" | head -1)
  other=$(grep -E "^Error:" "$out/$name.log" | grep -v -E "is violated|was violated|behavior up to this point" | head -1)
  case "$name" in
    expect-*)
      if [ -n "$violated" ] && [ -z "$other" ]; then echo "ok   $name: $violated"
      else echo "FAIL $name: expected a violation; see $out/$name.log"; status=1; fi ;;
    *)
      if [ -z "$violated" ] && [ -z "$other" ] && [ -n "$summary" ]; then echo "ok   $name: $summary"
      else echo "FAIL $name: ${violated:-$other}; see $out/$name.log"; status=1; fi ;;
  esac
done
exit $status
