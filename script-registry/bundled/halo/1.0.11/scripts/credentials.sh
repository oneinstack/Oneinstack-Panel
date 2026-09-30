#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
managed_installation_present || die "MANAGED_STATE_MISSING: Halo managed credentials are unavailable."
[[ -f "${environment_file}" && ! -L "${environment_file}" ]] ||
  die "CREDENTIAL_FILE_INVALID: Halo credential file must be a regular file."

credential_identity="$(stat -c '%u:%a:%G' -- "${environment_file}" 2>/dev/null || true)"
case "${credential_identity}" in
  "0:640:${run_group}") ;;
  *) die "CREDENTIAL_FILE_INSECURE: Halo credential file ownership or permissions are invalid." ;;
esac

credential_count="$(grep -c '^HALO_DATABASE_PASSWORD=' "${environment_file}" || true)"
[[ "${credential_count}" == 1 && -n "${database_password}" ]] ||
  die "CREDENTIAL_MISSING: Halo database password is not configured."
[[ "${database_password}" =~ ^[A-Za-z0-9._@%+=!#?-]{8,128}$ ]] ||
  die "CREDENTIAL_INVALID: Halo database password contains unsupported characters."

case "${database_type}" in
  h2) credential_username="admin" ;;
  postgresql|mysql|mariadb)
    validate_identifier "${database_username}" DATABASE_USERNAME
    credential_username="${database_username}"
    ;;
  *) die "CREDENTIAL_INVALID: Halo database type is unsupported." ;;
esac

printf 'component=halo\n'
printf 'credential.database-username=%s\n' "${credential_username}"
printf 'credential.database-password=%s\n' "${database_password}"
