# Bundled component packages

Release builds copy signed-off baseline component directories here using:

`<component>/<package-version>/manifest.yaml`, `files.sha256`, and action scripts.

The Panel prefers a verified package from Oneinstack-Center when enabled, then
falls back to the newest compatible package in this directory. The embedded
legacy scripts remain a temporary final fallback until their OneinStack
component packages have been extracted and accepted.

Current first-party baseline packages:

- Nginx 1.28.2
- MySQL 8.0 (upstream patch 8.0.45)
- PHP 8.1, 8.2, and 8.3
- Redis 7.4.8

They currently target Ubuntu 22.04/24.04 amd64. Run
`scripts/sync-center-components.sh` after changing the sibling
`Oneinstack-Center/components/production` sources.
