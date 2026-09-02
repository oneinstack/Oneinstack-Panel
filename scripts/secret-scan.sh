#!/usr/bin/env bash

set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
  echo "Secret scan must run inside a Git worktree" >&2
  exit 2
}

rules=(
  'private key|-----BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----'
  'GitHub token|gh[pousr]_[A-Za-z0-9]{36,255}'
  'GitHub fine-grained token|github_pat_[A-Za-z0-9_]{50,255}'
  'AWS access key|AKIA[0-9A-Z]{16}'
  'Tencent Cloud SecretId|AKID[A-Za-z0-9]{10,}'
  'Slack token|xox[baprs]-[A-Za-z0-9-]{20,255}'
  "non-empty config credential|^[[:space:]]*secret(Id|Key):[[:space:]]*(\"[^\"]+\"|'[^']+'|[^#[:space:]\"']+)"
)

pathspecs=(
  .
  ':(exclude)webui/app.zip'
  ':(exclude)dist/**'
  ':(exclude)packages/**'
  ':(exclude)coverage.out'
)

found=false
for rule in "${rules[@]}"; do
  label="${rule%%|*}"
  pattern="${rule#*|}"

  set +e
  matches="$(git grep -nI -E -e "$pattern" -- "${pathspecs[@]}" 2>/dev/null)"
  status=$?
  set -e

  case "$status" in
    0)
      printf 'Potential %s found:\n%s\n' "$label" "$matches" >&2
      found=true
      ;;
    1) ;;
    *)
      echo "git grep failed while scanning for ${label}" >&2
      exit "$status"
      ;;
  esac
done

if [[ "$found" == true ]]; then
  echo "Secret scan failed. Remove the credential and rotate it before continuing." >&2
  exit 1
fi

echo "Secret scan passed"
