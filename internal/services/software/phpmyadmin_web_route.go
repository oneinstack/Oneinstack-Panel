package software

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"oneinstack/internal/services/script"
)

var (
	phpMyAdminNginxRoot  = regexp.MustCompile(`(?m)^[[:space:]]*root[[:space:]]+([^;[:space:]]+);`)
	phpMyAdminApacheRoot = regexp.MustCompile(`(?m)^[[:space:]]*DocumentRoot[[:space:]]+"?([^"[:space:]]+)"?`)
	phpMyAdminCaddyRoot  = regexp.MustCompile(`(?m)^[[:space:]]*root[[:space:]]+\*[[:space:]]+([^[:space:]{}]+)`)
)

// preparePhpMyAdminWebRoute supplies server-owned values only for packages
// declaring this contract. Older immutable packages retain their own behavior.
func preparePhpMyAdminWebRoute(ctx context.Context, info *script.ScriptInfo) error {
	if info == nil || info.Name != "phpmyadmin" {
		return nil
	}
	declared := make(map[string]bool)
	for _, parameter := range info.ParameterSpecs {
		declared[strings.ToUpper(strings.TrimSpace(parameter.Name))] = true
	}
	if !declared["ONEINSTACK_WEB_SERVER_KIND"] || !declared["ONEINSTACK_WEB_DOCUMENT_ROOT"] {
		return nil
	}
	owners := ActiveRuntimeGroupOwners(ctx, webServerRuntimeGroup, "")
	if len(owners) == 0 {
		return &InstallParameterError{Field: "web-server", Message: "PHPMA_WEB_SERVER_NOT_FOUND"}
	}
	if len(owners) != 1 || !strings.HasPrefix(owners[0].ServiceName, "oneinstack-") {
		return &InstallParameterError{Field: "web-server", Message: "PHPMA_WEB_SERVER_AMBIGUOUS_OR_UNMANAGED"}
	}
	kind := owners[0].Component
	root, err := managedDefaultDocumentRoot(kind)
	if err != nil {
		return &InstallParameterError{Field: "web-server", Message: "PHPMA_DOCUMENT_ROOT_UNAVAILABLE"}
	}
	info.Params["ONEINSTACK_WEB_SERVER_KIND"] = kind
	info.Params["ONEINSTACK_WEB_DOCUMENT_ROOT"] = root
	return nil
}

func managedDefaultDocumentRoot(kind string) (string, error) {
	var path string
	var pattern *regexp.Regexp
	switch kind {
	case "nginx":
		path = filepath.Join(detectNginxInstallParameters()["install-dir"], "conf/conf.d/default.conf")
		pattern = phpMyAdminNginxRoot
	case "tengine":
		path = filepath.Join(detectTengineInstallParameters()["install-dir"], "conf/conf.d/default.conf")
		pattern = phpMyAdminNginxRoot
	case "openresty":
		path = filepath.Join(detectOpenRestyInstallParameters()["install-dir"], "nginx/conf/conf.d/default.conf")
		pattern = phpMyAdminNginxRoot
	case "apache":
		installDir := detectApacheInstallParameters()["install-dir"]
		path = filepath.Join(installDir, "conf/httpd.conf")
		if mainConfig, err := os.ReadFile(path); err == nil && strings.Contains(string(mainConfig), "oneinstack.conf") {
			for _, relative := range []string{"conf/extra/oneinstack.conf", "conf/oneinstack.conf"} {
				candidate := filepath.Join(installDir, relative)
				contents, err := os.ReadFile(candidate)
				if err == nil && phpMyAdminApacheRoot.Match(contents) {
					path = candidate
					break
				}
			}
		}
		pattern = phpMyAdminApacheRoot
	case "caddy":
		path = "/usr/local/caddy/conf/Caddyfile"
		mainConfig, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		managedPath := "/usr/local/caddy/conf/oneinstack-default.caddy"
		importPattern := regexp.MustCompile(`(?m)^[[:space:]]*import[[:space:]]+` + regexp.QuoteMeta(managedPath) + `[[:space:]]*$`)
		if importPattern.Match(mainConfig) {
			path = managedPath
		}
		pattern = phpMyAdminCaddyRoot
	default:
		return "", errors.New("unsupported managed web server")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	root := ""
	for _, match := range pattern.FindAllSubmatch(contents, -1) {
		candidate := string(match[1])
		if root != "" && candidate != root {
			return "", errors.New("ambiguous default document root")
		}
		root = candidate
	}
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || strings.ContainsAny(root, "\r\n\x00") {
		return "", errors.New("invalid default document root")
	}
	switch root {
	case "/usr", "/usr/local", "/etc", "/var", "/data", "/home", "/root":
		return "", errors.New("default document root is too broad")
	}
	if stat, err := os.Stat(root); err != nil || !stat.IsDir() {
		return "", errors.New("default document root is unavailable")
	}
	return root, nil
}
