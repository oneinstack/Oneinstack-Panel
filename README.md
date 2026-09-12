# OneinStack Panel

OneinStack Panel is an open-source Linux server operations panel for websites, software, databases, containers, files, certificates, security, monitoring, and auditing. Multiple independent Panel installations can be joined into a user-owned controller/node cluster.

> Center publishes Panel versions, component packages, and software-store catalogs. It is not part of the node runtime or task path.

## Features

- Resource monitoring: CPU, memory, disk, network, service health, and history
- Signed software catalog and component packages with install, upgrade, removal, and service configuration
- Website hosting and reverse proxy, Nginx configuration, certificates, ACME, backups, and restore
- Database connections, backups, restores, and task logs
- Docker containers, images, networks, volumes, Compose, and controlled terminals
- File manager, trash, SSH, firewall, Fail2ban, and scheduled tasks
- Operation preview, approvals, configuration snapshots, diffs, audit, alerts, and notifications
- Multi-node registration, token rotation, heartbeats, metrics, task queues, and website dispatch

## Multi-node mode

Deploy independent Panel instances and configure the role from **Multi-node Management**:

- **Controller** manages nodes, metrics, task dispatch, and website rollout.
- **Node** enables the node-mode switch and stores the controller URL and token in the backend configuration.
- Changes are applied by a runtime supervisor without manually editing YAML or restarting the service.

Supported task types are `software.install`, `software.uninstall`, `service.start`, `service.stop`, `service.restart`, `service.reload`, `system.command`, `file.upload`, `database.sync`, `website.sync`, and `website.content_sync`.

Website dispatch supports fixed nodes, tags, or least-load selection, with optional website-content transfer (up to 64 MiB). File uploads are limited to managed Panel directories and 16 MiB. Database dumps are limited to 64 MiB. System commands use argv arrays and a restricted allowlist; shells, downloaders, and interpreters are rejected.

Node endpoints accept only node tokens, while controller APIs require super-admin access. Tokens are stored as hashes and rotation immediately invalidates the previous token.

## Requirements

- Linux amd64 or arm64; verified on Ubuntu, Debian, CentOS, RHEL, Rocky, AlmaLinux, OpenCloudOS, and Anolis
- At least 1 GB RAM recommended and 20 GB free disk space
- Root privileges, systemd, and `prlimit`

## Install and update

Download the architecture-matched release archive and checksum, verify it in a temporary directory, then run the installer:

```bash
VERSION="v0.3.0-build.11"
PACKAGE="one-linux-amd64-${VERSION}.tar.gz"
BASE_URL="https://mirrors.oneinstack.com/oneinstack"
work_dir="$(mktemp -d)"; trap 'rm -rf -- "$work_dir"' EXIT; cd "$work_dir"
wget -c "$BASE_URL/$PACKAGE" "$BASE_URL/$PACKAGE.sha256"
sha256sum -c "$PACKAGE.sha256" && tar -xzf "$PACKAGE"
cd "${PACKAGE%.tar.gz}" && sudo ./install.sh --force
```

Open `http://your-server-ip:8089` after installation. HTTP remains available by default; HTTPS can be configured separately in Settings. Run `sudo ./install.sh --force` with another verified release to update while preserving configuration.

Normal uninstall: `sudo ./install.sh uninstall`.

Permanent removal requires explicit confirmation: `sudo ./install.sh uninstall --purge --yes`.

## Center and software store

Center controls release channels, rollout, software versions, and component packages. Panel applies updates only after verifying signatures, revision digests, artifact sizes, and SHA-256. When Center is unavailable, the last verified catalog remains usable.

```yaml
scriptCenter:
  enabled: true
  url: "https://center.example.com"
  channel: "stable"
  trustedKeys:
    center-key-id: "BASE64_ED25519_PUBLIC_KEY"
updateCenter:
  enabled: true
  centerUrl: "https://center.example.com"
```

Production Center connections must use HTTPS. See [BUILD.md](BUILD.md), [CLUSTER_MANAGEMENT.md](CLUSTER_MANAGEMENT.md), and [docs/cluster-management.md](docs/cluster-management.md) for build, release, and API details.

## Development and license

```bash
go test ./...
go run ./cmd server
```

Built with Go, Gin, GORM, SQLite, Systemd, and Vue.js. Licensed under [Apache License 2.0](LICENSE).

Website: [oneinstack.com](https://oneinstack.com) · Issues: [GitHub Issues](https://github.com/oneinstack/Oneinstack-Panel/issues)
