#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
	echo "usage: $0 <base-ref>" >&2
	exit 2
fi

repo_root=$(git rev-parse --show-toplevel)
base_ref=$1
base_commit=$(git merge-base HEAD "$base_ref")
status=0

while IFS=$'\t' read -r change old_path new_path; do
	[[ -n "$change" ]] || continue
	case "$change" in
	A*)
		;;
	R*)
		echo "migration history is append-only: $old_path was renamed to $new_path ($change)" >&2
		status=1
		;;
	*)
		echo "migration history is append-only: $old_path has disallowed change $change" >&2
		status=1
		;;
	esac
done < <(git -C "$repo_root" diff --name-status --find-renames "$base_commit" HEAD -- 'internal/store/sql/*.sql')

if [[ $status -ne 0 ]]; then
	echo "add a new migration instead of changing, deleting, or renaming an existing one" >&2
	exit "$status"
fi

echo "migration immutability: only additions relative to $base_ref ($base_commit)"
