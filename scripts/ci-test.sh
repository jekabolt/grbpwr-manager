#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "usage: scripts/ci-test.sh [--update]" >&2
  exit 2
}

update_baseline=0
if (( $# > 1 )); then
  usage
fi
if (( $# == 1 )); then
  [[ "$1" == "--update" ]] || usage
  update_baseline=1
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
baseline="$repo_dir/internal/store/known-red.txt"
# Non-store tests red on beta when the gate was introduced (25.10.26); same shrink-only ratchet.
unit_baseline="$repo_dir/scripts/known-red-unit.txt"
test_db_script=${TEST_DB_SCRIPT:-$HOME/.claude/scripts/test-db.sh}

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/grbpwr-ci-test.XXXXXX")
cleanup() {
  rm -rf -- "$tmp_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

log() {
  printf '%s\n' "ci-test.sh: $*"
}

run_timed() {
  local label=$1
  local started=$SECONDS
  shift
  log "starting $label"
  "$@"
  log "$label completed in $((SECONDS - started))s"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ci-test.sh: required command not found: $1" >&2
    exit 2
  }
}

generate_sources() {
  require_command buf
  require_command mockery
  require_command oapi-codegen

  (
    cd -- "$repo_dir"
    buf generate
    make generate-resend-client generate-mocks
  )
}

run_non_store_tests() {
  local package
  local -a packages=()

  (
    cd -- "$repo_dir"
    go list ./...
  ) >"$tmp_dir/packages.txt"

  while IFS= read -r package; do
    case "$package" in
      */internal/store|*/internal/store/*) ;;
      *) packages+=("$package") ;;
    esac
  done <"$tmp_dir/packages.txt"

  if (( ${#packages[@]} == 0 )); then
    echo "ci-test.sh: go list returned no non-store packages" >&2
    return 1
  fi

  log "running ${#packages[@]} non-store packages"
  local unit_status
  set +e
  (
    cd -- "$repo_dir"
    go test -json -count=1 "${packages[@]}"
  ) >"$tmp_dir/unit.json" 2>&1
  unit_status=$?
  set -e
  classify_json "unit" "$tmp_dir/unit.json" "$unit_status" "$tmp_dir/unit-failed.txt" 1
}

# classify_json <label> <go-test-json> <exit-status> <failed-names-out> [qualify]
# qualify=1 writes "<package without module prefix> <Test>" so equal test names in different
# packages cannot mask each other.
classify_json() {
  local label=$1 source=$2 status=$3 failed=$4 qualify=${5:-0}
  python3 - "$source" "$failed" "$tmp_dir/$label-unclassified.txt" "$qualify" <<'PY2'
import json
import sys

source, failed_path, unclassified_path, qualify = sys.argv[1:]
module = "github.com/jekabolt/grbpwr-manager/"
failed_tests = set()
failed_packages = set()
packages_with_failed_tests = set()

with open(source, encoding="utf-8", errors="replace") as stream:
    for line in stream:
        try:
            event = json.loads(line)
        except (json.JSONDecodeError, TypeError):
            continue
        if not isinstance(event, dict) or event.get("Action") != "fail":
            continue
        package = event.get("Package")
        test = event.get("Test")
        if test:
            if qualify == "1" and package:
                failed_tests.add(package.removeprefix(module) + " " + test)
            else:
                failed_tests.add(test)
            if package:
                packages_with_failed_tests.add(package)
        elif package:
            failed_packages.add(package)

with open(failed_path, "w", encoding="utf-8") as stream:
    for test in sorted(failed_tests):
        stream.write(test + "\n")

with open(unclassified_path, "w", encoding="utf-8") as stream:
    for package in sorted(failed_packages - packages_with_failed_tests):
        stream.write(package + "\n")
PY2
  if [[ -s "$tmp_dir/$label-unclassified.txt" ]]; then
    echo "ci-test.sh: $label packages failed without a failing test event (build error?):" >&2
    sed 's/^/  /' "$tmp_dir/$label-unclassified.txt" >&2
    grep -v '^{' "$source" >&2 || true
    return 1
  fi
  if (( status != 0 )) && [[ ! -s "$failed" ]]; then
    echo "ci-test.sh: $label command failed with exit $status but reported no failing test" >&2
    grep -v '^{' "$source" >&2 || true
    return 1
  fi
  if (( status == 0 )) && [[ -s "$failed" ]]; then
    echo "ci-test.sh: $label command exited 0 despite failing test events" >&2
    return 1
  fi
  log "$label suite reported $(wc -l <"$failed" | tr -d ' ') failing test names"
}

run_store_tests() {
  local present=0
  local variable
  local store_status
  local -a mysql_variables=(MYSQL_USER MYSQL_PASSWORD MYSQL_HOST MYSQL_PORT MYSQL_DATABASE)

  for variable in "${mysql_variables[@]}"; do
    if [[ -n "${!variable:-}" ]]; then
      present=$((present + 1))
    fi
  done

  set +e
  if (( present == 0 )); then
    if [[ ! -x "$test_db_script" ]]; then
      echo "ci-test.sh: no MYSQL_* test environment is set and helper is not executable: $test_db_script" >&2
      set -e
      return 2
    fi
    log "no MYSQL_* test environment detected; using $test_db_script"
    "$test_db_script" -C "$repo_dir" -- -json ./internal/store/... -count=1 \
      >"$tmp_dir/store.json" 2>&1
    store_status=$?
  elif (( present != ${#mysql_variables[@]} )); then
    set -e
    echo "ci-test.sh: incomplete MYSQL_* test environment; set all of MYSQL_USER, MYSQL_PASSWORD, MYSQL_HOST, MYSQL_PORT, and MYSQL_DATABASE, or none of them" >&2
    return 2
  else
    log "using MYSQL_* test environment for ${MYSQL_DATABASE} at ${MYSQL_HOST}:${MYSQL_PORT}"
    (
      cd -- "$repo_dir"
      CI=1 go test -json ./internal/store/... -count=1
    ) >"$tmp_dir/store.json" 2>&1
    store_status=$?
  fi
  set -e

  classify_json "store" "$tmp_dir/store.json" "$store_status" "$tmp_dir/failed-tests.txt"
}

validate_baseline() {
  local file=$1
  LC_ALL=C sort -u "$file" | awk 'NF' >"$tmp_dir/normalized-baseline.txt"
  if ! cmp -s "$file" "$tmp_dir/normalized-baseline.txt"; then
    echo "ci-test.sh: $file must contain sorted, unique, non-empty test names" >&2
    return 1
  fi
}

# print_failure_output <go-test-json> <names>: the test output of each named failure
print_failure_output() {
  python3 - "$1" "$2" <<'PY2'
import json
import sys

module = "github.com/jekabolt/grbpwr-manager/"
names = {line.strip() for line in open(sys.argv[2], encoding="utf-8") if line.strip()}
with open(sys.argv[1], encoding="utf-8", errors="replace") as stream:
    for line in stream:
        try:
            event = json.loads(line)
        except (json.JSONDecodeError, TypeError):
            continue
        if not isinstance(event, dict) or event.get("Action") != "output" or not event.get("Test"):
            continue
        package = (event.get("Package") or "").removeprefix(module)
        if event["Test"] in names or f"{package} {event['Test']}" in names:
            sys.stdout.write(f"  | {package} {event['Test']}: {event.get('Output', '')}")
PY2
}

# compare_failures <label> <failed-names> <baseline-file>
# New red outside the baseline -> exit 1 with names. Baseline may only shrink.
compare_failures() {
  local label=$1 failed=$2 file=$3 rel
  rel=${file#"$repo_dir"/}
  LC_ALL=C sort -u "$failed" -o "$failed"
  if [[ -f "$file" ]]; then
    validate_baseline "$file"
    LC_ALL=C comm -23 "$failed" "$file" >"$tmp_dir/$label-new.txt"
    LC_ALL=C comm -13 "$failed" "$file" >"$tmp_dir/$label-recovered.txt"
  else
    : >"$tmp_dir/$label-new.txt"
    : >"$tmp_dir/$label-recovered.txt"
    if (( update_baseline == 0 )); then
      echo "ci-test.sh: missing $label baseline: $rel (run scripts/ci-test.sh --update)" >&2
      return 1
    fi
    cp "$failed" "$file"
    log "created $rel ($(awk 'NF' "$file" | wc -l | tr -d ' ') names)"
    return 0
  fi

  if [[ -s "$tmp_dir/$label-new.txt" ]]; then
    echo "ci-test.sh: new $label test failures:" >&2
    while IFS= read -r test; do
      printf 'new %s failure: %s\n' "$label" "$test" >&2
    done <"$tmp_dir/$label-new.txt"
    print_failure_output "$tmp_dir/$label.json" "$tmp_dir/$label-new.txt" >&2
    echo "ci-test.sh: refusing to add failures to $rel" >&2
    return 1
  fi

  while IFS= read -r test; do
    [[ -n "$test" ]] || continue
    printf 'можно убрать из %s: %s\n' "$rel" "$test"
  done <"$tmp_dir/$label-recovered.txt"

  if (( update_baseline == 1 )); then
    cp "$failed" "$file"
    log "updated $rel ($(awk 'NF' "$file" | wc -l | tr -d ' ') names)"
  fi
}

cd -- "$repo_dir"
total_started=$SECONDS
run_timed "source generation" generate_sources
run_timed "go vet ./..." go vet ./...
run_timed "non-store tests" run_non_store_tests
compare_failures "unit" "$tmp_dir/unit-failed.txt" "$unit_baseline"
run_timed "store tests" run_store_tests
compare_failures "store" "$tmp_dir/failed-tests.txt" "$baseline"
log "all gates completed in $((SECONDS - total_started))s"
