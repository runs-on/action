#!/usr/bin/env bash
set -euo pipefail

ENC_KEY=${ENC_KEY:-}
CACHE_DIR=${CACHE_DIR:-}
WORKSPACE=${WORKSPACE:-${GITHUB_WORKSPACE:-}}
FORCE_WRITE=${FORCE_WRITE:-false}

if [[ -z "${CACHE_DIR}" || -z "${WORKSPACE}" ]]; then
  echo "Checkout cache save skipped: CACHE_DIR or WORKSPACE is not set"
  exit 0
fi

mkdir -p "${CACHE_DIR}"

sha="$(git -C "${WORKSPACE}" rev-parse HEAD)"
match="$(find "${CACHE_DIR}" -maxdepth 1 -type f -name "repository-${sha}-*.tar" -exec basename {} \; | sort -t '-' -k3,3nr | head -n1 || true)"
if [[ -n "${match}" && "${FORCE_WRITE}" != "true" ]]; then
  echo "Checkout cache for ${sha} already exists; skipping save"
  exit 0
fi

while IFS= read -r key; do
  [[ -n "${key}" ]] || continue
  git -C "${WORKSPACE}" config --local --unset-all "${key}" || true
done < <(git -C "${WORKSPACE}" config --local --name-only --get-regexp '^http\..*\.extraheader$' || true)

file_location="${CACHE_DIR}/repository-${sha}-$(date +%s).tar"
tmp_file="${file_location}.tmp"
rm -f "${tmp_file}"

echo "Saving checkout cache to ${file_location}"
if [[ -n "${ENC_KEY}" ]]; then
  tar -C "${WORKSPACE}" -cf - .git | openssl enc -aes-256-cbc -salt -pbkdf2 -pass env:ENC_KEY -out "${tmp_file}"
else
  tar -C "${WORKSPACE}" -cf "${tmp_file}" .git
fi

mv "${tmp_file}" "${file_location}"
