# Apache HTTP Server stable package

Package version: 1.0.17. Supported software versions: 2.4.66.

This package is generated from the OneinStack component matrix and invokes the
non-interactive installer from pinned commit 42d59b33765ad57c455b83bc3d4eb09ed367754a. The upstream archive and any
component-specific source overrides are verified before use; the baseline
archive SHA-256 is 65a9164f7d9b6037e0771b28cbd04baec0207c28f556dd78b773853898b53dfa.

## Lifecycle

The scripts validate required inputs and host constraints, snapshot an existing
managed installation, run the pinned installer, normalize runtime ownership,
prepare a managed service when applicable, and verify the installed version and
runtime state. Rollback restores only a captured transaction. Uninstall removes
only component-owned resources and preserves component data by default.

These generic adapters support Center/online installation. They reject offline
mode because a complete immutable bundle for every upstream dependency is not
defined. phpMyAdmin uses a separate verified offline lifecycle documented in
its own package.

## Validation boundary

The manifest is the authoritative compatibility declaration. Generation,
syntax checks, and package creation are static evidence only; each target host
must still validate dependency availability, installation, repeat installation,
service actions where applicable, rollback, uninstall, and the component's
native runtime probe.
