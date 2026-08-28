#!/usr/bin/env bash
set -euo pipefail

GITHUB_OUTPUT=${GITHUB_OUTPUT:-/dev/null}
EFS_MOUNT_PATH=${EFS_MOUNT_PATH:-/mnt/efs}

enabled=false
if [[ "${RUNNER_OS:-}" == "Linux" ]] && [[ -n "${RUNS_ON_RUNNER_NAME:-}" ]]; then
  if [[ -n "${RUNS_ON_EFS_ID:-}" ]] || [[ -d "${EFS_MOUNT_PATH}" && -w "${EFS_MOUNT_PATH}" ]]; then
    enabled=true
  fi
fi

{
  echo "enabled=${enabled}"
  echo "mount_path=${EFS_MOUNT_PATH}"
} | tee -a "${GITHUB_OUTPUT}"
