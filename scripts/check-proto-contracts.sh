#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CONTRACT_DIR="$ROOT/proto/contracts"
MIRROR_DIR=${PROTO_MIRROR_DIR:-"$ROOT/../grbpwr-proto"}
BREAKING_REF=${PROTO_BREAKING_AGAINST:-$(sed -n '1p' "$CONTRACT_DIR/released-git-ref.txt")}
MIRROR_REF=$(sed -n '1p' "$CONTRACT_DIR/mirror-git-ref.txt")
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/grbpwr-proto-contracts.XXXXXX")

trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM
cd "$ROOT"

normalize_violations() {
	sed -E 's/^\{"path":"([^"]+)",.*"type":"([^"]+)","message":"(.*)"\}$/\2|\1|\3/' | LC_ALL=C sort
}

run_buf_check() {
	output_file=$1
	shift
	status=0
	"$@" >"$output_file" 2>"$output_file.stderr" || status=$?
	if [ "$status" -ne 0 ] && [ "$status" -ne 100 ]; then
		cat "$output_file.stderr" >&2
		echo "proto contract command failed with exit code $status: $*" >&2
		exit "$status"
	fi
}

hash_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

format_diff=$(buf format --diff)
if [ -n "$format_diff" ]; then
	printf '%s\n' "$format_diff" >&2
	echo "proto files are not formatted; run make format-proto" >&2
	exit 1
fi

buf build

run_buf_check "$TMP_DIR/lint.raw" buf lint --error-format=json
normalize_violations <"$TMP_DIR/lint.raw" >"$TMP_DIR/lint.actual"
if ! diff -u "$CONTRACT_DIR/lint-baseline.txt" "$TMP_DIR/lint.actual"; then
	echo "proto lint baseline changed; fix new violations or explicitly review the baseline" >&2
	exit 1
fi

: >"$TMP_DIR/breaking.raw"
if ! git cat-file -e "$BREAKING_REF^{commit}" 2>/dev/null; then
	echo "released proto baseline is not a readable commit: $BREAKING_REF" >&2
	exit 1
fi
mkdir -p "$TMP_DIR/released"
git archive "$BREAKING_REF" proto buf.work.yaml | tar -x -C "$TMP_DIR/released"
for module in admin auth common frontend; do
	(
		cd "$TMP_DIR/released"
		buf build "proto/$module" -o "$TMP_DIR/released-$module.binpb"
	)
	run_buf_check "$TMP_DIR/breaking-$module.raw" \
		buf breaking "proto/$module" \
		--against "$TMP_DIR/released-$module.binpb" \
		--exclude-imports \
		--error-format=json
	cat "$TMP_DIR/breaking-$module.raw" >>"$TMP_DIR/breaking.raw"
done
normalize_violations <"$TMP_DIR/breaking.raw" >"$TMP_DIR/breaking.actual"
if ! diff -u "$CONTRACT_DIR/breaking-baseline.txt" "$TMP_DIR/breaking.actual"; then
	echo "breaking-change set differs from the approved release manifest" >&2
	exit 1
fi

if [ ! -d "$MIRROR_DIR" ]; then
	echo "proto mirror not found at $MIRROR_DIR; set PROTO_MIRROR_DIR" >&2
	exit 1
fi
actual_mirror_ref=$(git -C "$MIRROR_DIR" rev-parse HEAD 2>/dev/null || true)
if [ "$actual_mirror_ref" != "$MIRROR_REF" ]; then
	echo "proto mirror must be checked out at $MIRROR_REF, got ${actual_mirror_ref:-non-git directory}" >&2
	exit 1
fi

find "$ROOT/proto" -type f -name '*.proto' | sed "s|^$ROOT/proto/||" | LC_ALL=C sort >"$TMP_DIR/backend.files"
find "$MIRROR_DIR" -type f -name '*.proto' | sed "s|^$MIRROR_DIR/||" | LC_ALL=C sort >"$TMP_DIR/mirror.files"
if ! diff -u "$TMP_DIR/backend.files" "$TMP_DIR/mirror.files"; then
	echo "backend and mirror expose different proto file sets" >&2
	exit 1
fi

: >"$TMP_DIR/mirror.actual"
while IFS= read -r relative_path; do
	backend_file="$ROOT/proto/$relative_path"
	mirror_file="$MIRROR_DIR/$relative_path"
	if ! cmp -s "$backend_file" "$mirror_file"; then
		printf '%s|%s|%s\n' \
			"$relative_path" \
			"$(hash_file "$backend_file")" \
			"$(hash_file "$mirror_file")" >>"$TMP_DIR/mirror.actual"
	fi
done <"$TMP_DIR/backend.files"
LC_ALL=C sort -o "$TMP_DIR/mirror.actual" "$TMP_DIR/mirror.actual"

if ! diff -u "$CONTRACT_DIR/mirror-allowlist.txt" "$TMP_DIR/mirror.actual"; then
	echo "backend/mirror drift changed; synchronize the mirror or explicitly review the SHA allowlist" >&2
	exit 1
fi

echo "proto contract checks passed"
