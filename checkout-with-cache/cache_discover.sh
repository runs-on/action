#!/usr/bin/env bash
set -euo pipefail

GITHUB_OUTPUT=${GITHUB_OUTPUT:-/dev/null}
CACHE_DIR=${CACHE_DIR:-}
EFS_ENABLED=${EFS_ENABLED:-false}

if [[ -z "${CACHE_DIR}" ]]; then
  CACHE_DIR="/mnt/efs/${GITHUB_REPOSITORY}/.git"
fi

latest_file=""
latest_path=""
if [[ "${EFS_ENABLED}" == "true" && -d "${CACHE_DIR}" ]]; then
  CACHE_DIR=$(realpath "${CACHE_DIR}")
  latest_file=$(find "${CACHE_DIR}" -maxdepth 1 -type f -name 'repository-*.tar' -exec basename {} \; | sort -t '-' -k3,3nr | head -n1 || true)
  if [[ -n "${latest_file}" ]]; then
    latest_path="${CACHE_DIR}/${latest_file}"
  fi
fi

if [[ -n "${latest_file}" ]]; then
  cache_hit=true
else
  cache_hit=false
fi

{
  echo "enabled=${EFS_ENABLED}"
  echo "cache_hit=${cache_hit}"
  echo "cache_dir=${CACHE_DIR}"
  echo "latest_file_name=${latest_file}"
  echo "latest_file_path=${latest_path}"
} | tee -a "${GITHUB_OUTPUT}"
