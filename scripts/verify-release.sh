#!/usr/bin/env bash

set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "Usage: $0 ARCHIVE [ARCHIVE...]" >&2
  exit 2
fi

for archive in "$@"; do
  if [[ ! -f "$archive" ]]; then
    echo "Archive not found: $archive" >&2
    exit 1
  fi

  checksum="${archive}.sha256"
  if [[ ! -f "$checksum" ]]; then
    echo "Checksum not found: $checksum" >&2
    exit 1
  fi

  archive_dir="$(cd "$(dirname "$archive")" && pwd)"
  archive_name="$(basename "$archive")"
  (
    cd "$archive_dir"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c "$(basename "$checksum")"
    else
      expected="$(awk '{print $1}' "$(basename "$checksum")")"
      actual="$(shasum -a 256 "$archive_name" | awk '{print $1}')"
      [[ "$actual" == "$expected" ]]
    fi
  )

  listing="$(tar -tzf "$archive")"
  root="${listing%%/*}"
  for required in one config.yaml install.sh install-cent.sh install-ubuntu.sh README.md README-zh.md BUILD.md LICENSE; do
    if ! grep -Fxq "${root}/${required}" <<<"$listing"; then
      echo "Missing ${root}/${required} in $archive" >&2
      exit 1
    fi
  done
  if ! grep -Fq "${root}/script-registry/bundled/" <<<"$listing"; then
    echo "Missing ${root}/script-registry/bundled in $archive" >&2
    exit 1
  fi
  if tar -xOf "$archive" "${root}/config.yaml" |
    grep -Eq "^[[:space:]]*secret(Id|Key):[[:space:]]*(\"[^\"]+\"|'[^']+'|[^#[:space:]\"']+)"; then
    echo "Release config contains a non-empty credential: $archive" >&2
    exit 1
  fi

  echo "Verified ${archive}"
done
