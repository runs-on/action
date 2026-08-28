#!/usr/bin/env bash
set -euo pipefail

ENC_KEY=${ENC_KEY:-}
CACHE_FILE=${CACHE_FILE:?CACHE_FILE is required}
WORKSPACE=${WORKSPACE:-${GITHUB_WORKSPACE:-}}

if [[ -z "${WORKSPACE}" ]]; then
  echo "Checkout cache restore skipped: WORKSPACE is not set"
  exit 0
fi

echo "Restoring checkout cache from ${CACHE_FILE} into ${WORKSPACE}"
mkdir -p "${WORKSPACE}"

restore_ok=false
if [[ -n "${ENC_KEY}" ]]; then
  if openssl enc -aes-256-cbc -pbkdf2 -d -pass env:ENC_KEY -in "${CACHE_FILE}" | tar -xf - -C "${WORKSPACE}"; then
    restore_ok=true
  fi
else
  if tar -xf "${CACHE_FILE}" -C "${WORKSPACE}"; then
    restore_ok=true
  fi
fi

if [[ "${restore_ok}" != "true" || ! -d "${WORKSPACE}/.git" ]]; then
  echo "Checkout cache restore failed or did not contain .git; falling back to a normal checkout"
  rm -rf "${WORKSPACE}/.git"
  exit 0
fi

echo "Reconciling restored checkout cache"
git -C "${WORKSPACE}" update-index --refresh || true
git -C "${WORKSPACE}" reset --hard HEAD || true
