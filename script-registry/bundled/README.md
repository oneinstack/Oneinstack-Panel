# Bundled component packages

`production-lock.json` selects the 22 stable packages shipped with Panel:
Adminer, Apache, Caddy, ClamAV, Docker, Docker Compose, Fail2ban, firewalld,
Halo, MariaDB, MongoDB, MySQL, Nginx, Node.js, OpenResty, OpenSearch, PHP,
phpMyAdmin, Redis, Tengine, Tomcat, and WebDAV. Grafana, Loki, and MinIO are
excluded. It records the committed Center source revision, package
versions, and deterministic content digests. Older package directories remain
available for installed component lifecycle actions.
`production-catalog.json` contains the 22 matching software-store products
from the same committed Center source, including versions, parameters, and
icons. A fresh Panel can display this local catalog before Center sync.

Run `scripts/sync-center-components.sh [CENTER_PATH] [CENTER_COMMIT]` to build
all 22 packages from one committed Center revision. The script rejects a
different package under an existing component version. Panel release scripts
verify the lock, catalog, and package contents before packaging and after extraction.

Center is preferred for new installs. During a Center service outage, Panel
uses a compatible locked package when the requested software version is in a
verified Center catalog or the matching bundled production catalog. The latter
is identified as `bundled`, not as a Center-signed package. The bundled scripts
still download software and system dependencies as part of online installation;
they are not offline software bundles. Host support is determined by each
package manifest and the target host, not by this directory as a whole.
