#!/usr/bin/env bash
set -euo pipefail

CACHE_DIR=${CACHE_DIR:-}
KEEP_FILES_COUNT=${KEEP_FILES_COUNT:-5}

if [[ -z "${CACHE_DIR}" || ! -d "${CACHE_DIR}" ]]; then
  exit 0
fi

mapfile -t files < <(find "${CACHE_DIR}" -maxdepth 1 -type f -name 'repository-*.tar' -exec basename {} \; | sort -t '-' -k3,3nr)
if (( ${#files[@]} <= KEEP_FILES_COUNT )); then
  exit 0
fi

for file in "${files[@]:KEEP_FILES_COUNT}"; do
  rm -vf "${CACHE_DIR}/${file}"
done
