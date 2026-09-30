# Node.js runtime package

Package version: 1.0.8. Runtime versions: 22.12.0 and 16.20.2.

The component package contains scripts and a manifest. Center/online installs
download from pinned official Node.js URLs and verify SHA-256. Offline installs
read the matching artifact from `artifacts/<architecture>/` without network
access:

| Artifact | SHA-256 |
| --- | --- |
| `node-v16.20.2-linux-x64.tar.xz` | 874463523f26ed528634580247f403d200ba17a31adf2de98a7b124c6eb33d87 |
| `node-v16.20.2-linux-arm64.tar.xz` | e88d86154d1ce53dc52fd74d79d4bfdf0b05f58c0bb2639adfa36e9378b770c4 |
| `node-v22.12.0-linux-x64.tar.xz` | 22982235e1b71fa8850f82edd09cdae7e3f32df1764a9ec298c72d25ef2c164f |
| `node-v22.12.0-linux-arm64.tar.xz` | 8cfd5a8b9afae5a2e0bd86b0148ca31d2589c0ea669c2d0b11c132e35d90ed68 |
| `node-v22.12.0.tar.xz` | fe1bc4be004dc12721ea2cb671b08a21de01c6976960ef8a1248798589679e16 |

Expected offline-bundle paths:

```text
artifacts/amd64/node-v16.20.2-linux-x64.tar.xz
artifacts/arm64/node-v16.20.2-linux-arm64.tar.xz
artifacts/amd64/node-v22.12.0-linux-x64.tar.xz
artifacts/arm64/node-v22.12.0-linux-arm64.tar.xz
artifacts/amd64/node-v22.12.0.tar.xz
artifacts/arm64/node-v22.12.0.tar.xz
packages/centos/7/amd64/*.rpm
packages/centos/7/arm64/*.rpm
```

## Compatibility matrix

The Node.js 22.12.0 official-binary route declares Ubuntu 22.04/24.04/26.04,
Debian 11/12/13, RHEL/Rocky/AlmaLinux/Oracle Linux 8/9/10,
CentOS 8/9/10, Fedora, Amazon Linux 2023, SLES, and openSUSE on amd64
and arm64.

On CentOS 7, Node.js 22.12.0 is compiled locally from the Center-managed source
archive and bundled build dependencies. Node.js 16.20.2 uses the official
glibc 2.17 binary as a legacy CentOS 7 compatibility route and does not trigger
a local source build. Version 16.20.2 is EOL and is not recommended for new
production deployments.

Wildcard versions in the manifest only permit matching. Repository checks
provide static script and artifact evidence; they do not prove real-host
acceptance for every declared OS, version, and architecture.

## Installation parameters

- `SOFTWARE_VERSION` is required, accepts `22.12.0` or `16.20.2`, and defaults
  to `22.12.0`.
- `INSTALL_DIR` defaults to `/usr/local/node`.
- `TAKEOVER_UNMANAGED_CONFLICTS` defaults to `false`. When enabled, the scripts
  back up an unmanaged `INSTALL_DIR`, conflicting
  `/usr/local/bin/{node,npm,npx,corepack,nodejs}` entries, and
  `/etc/profile.d/nodejs.sh` before takeover. The installation-directory backup
  is stored beside `INSTALL_DIR` as `.nodejs-conflict-backup.<suffix>` and its
  exact path is recorded in `installed.json`; failed installs restore it
  transactionally, and uninstall restores it while the replacement remains
  managed by OneinStack.
- `ONEINSTACK_COMPONENT_STATE` defaults to `/var/lib/oneinstack/components`.
- `ONEINSTACK_INSTALL_MODE` and `ONEINSTACK_OFFLINE_PACKAGE_PATH` are internal
  values injected by Panel and are not user-editable parameters.

Node.js does not create a systemd service. The lifecycle manages PATH and the
`node`, `npm`, `npx`, and `corepack` entrypoints. Uninstall removes only the
managed runtime and entrypoints; it does not delete application directories.

## Validation boundary

Each target host must still validate artifact selection, dependency
availability, runtime installation, command entrypoints, rollback behavior,
and the reported Node.js version.
