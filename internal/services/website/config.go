package website

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"oneinstack/internal/models"
)

const (
	defaultNginxConfigDir = "/usr/local/nginx/conf/conf.d"
	defaultNginxBinary    = "/usr/local/nginx/sbin/nginx"
	defaultChallengeRoot  = "/usr/local/one/acme-webroot"
	// publishCommandTimeout bounds each engine validation/reload command during
	// Publish and Rollback. Caddy validate or reload can block
	// indefinitely; a bounded context prevents one stuck command from holding
	// the operation preview or task locks.
	publishCommandTimeout = 20 * time.Second
	// Rollback is best effort, but must not turn a failed publication into an
	// unbounded request. Runtime commands remain guarded by the shorter timeout.
	publishRollbackTimeout = 10 * time.Second
)

var (
	ErrWebServerUnavailable     = errors.New("supported web server unavailable")
	ErrWebServerConfigConflict  = errors.New("web server configuration revision conflict")
	ErrWebServerConfigValidate  = errors.New("web server configuration validation failed")
	ErrWebsiteConflict          = errors.New("website conflict")
	ErrWebsiteExpired           = errors.New("website expired")
	ErrWebsiteIDRequired        = errors.New("website ID is required")
	ErrWebsiteParameterInvalid  = errors.New("website parameter invalid")
	ErrWebsiteSettingsValidate  = errors.New("website settings validation failed")
	ErrWebsiteRootInvalid       = errors.New("website root path is invalid")
	ErrWebsiteWebServerMismatch = errors.New("WEBSITE_WEB_SERVER_MISMATCH")
	ErrWebsiteEngineImmutable   = errors.New("WEBSITE_ENGINE_IMMUTABLE")
	ErrWebsiteConfigUnavailable = errors.New("WEBSITE_CONFIG_UNAVAILABLE")
	ErrNginxUnavailable         = ErrWebServerUnavailable
	domainLabelPattern          = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	configNamePattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,252}\.conf$`)
)

// Caddy has one admin endpoint per running instance. Serialize reloads so a
// second website operation cannot enqueue another reload while the first one
// is still being handled by Caddy.
var caddyReloadSlot = make(chan struct{}, 1)

func wrapWebsiteParameterError(err error) error {
	if err == nil || errors.Is(err, ErrWebsiteRootInvalid) ||
		errors.Is(err, ErrWebsiteSettingsValidate) || errors.Is(err, ErrWebsiteParameterInvalid) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrWebsiteParameterInvalid, err)
}

func resolveNginxBinary() (string, error) {
	server, err := DetectWebServer()
	if err != nil {
		return "", err
	}
	return server.BinaryPath, nil
}

type siteTemplateData struct {
	Name              string
	ListenPort        int
	ServerNames       string
	RootDir           string
	ProxyURL          string
	ProxyHost         string
	Remark            string
	LogDir            string
	LogName           string
	ChallengeRoot     string
	TLSEnabled        bool
	ForceHTTPS        bool
	CertPath          string
	KeyPath           string
	DefaultDocuments  string
	AutoIndex         string
	PHPBackend        string
	RewriteDirectives string
	ServerDirectives  string
	ExtraLocations    string
	AccessLogEnabled  bool
	ErrorLogEnabled   bool
}

var siteTemplates = template.Must(template.New("nginx-sites").Parse(`
{{define "challenge"}}
    location ^~ /.well-known/acme-challenge/ {
        root {{.ChallengeRoot}};
        default_type text/plain;
        allow all;
        try_files $uri =404;
    }
{{end}}
{{define "tls"}}
    ssl_certificate {{.CertPath}};
    ssl_certificate_key {{.KeyPath}};
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:OneinStackSSL:10m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;
{{if .ForceHTTPS}}
    add_header Strict-Transport-Security "max-age=31536000" always;
{{end}}
{{end}}
{{define "logs"}}
    # {{.Remark}}
{{if .AccessLogEnabled}}
    access_log {{.LogDir}}/{{.LogName}}_access.log;
{{else}}
    access_log off;
{{end}}
{{if .ErrorLogEnabled}}
    error_log {{.LogDir}}/{{.LogName}}_error.log;
{{else}}
    error_log /dev/null crit;
{{end}}
{{end}}
{{define "php-content"}}
    root {{.RootDir}};
    index {{.DefaultDocuments}};
    autoindex {{.AutoIndex}};

    location / {
        try_files $uri $uri/ =404;
{{.RewriteDirectives}}
    }

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_pass {{.PHPBackend}};
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
    }
{{end}}
{{define "proxy-content"}}
    location / {
        proxy_pass {{.ProxyURL}};
        proxy_set_header Host {{.ProxyHost}};
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
{{end}}
{{define "static-content"}}
    root {{.RootDir}};
    index {{.DefaultDocuments}};
    autoindex {{.AutoIndex}};

    location / {
        try_files $uri $uri/ =404;
{{.RewriteDirectives}}
    }
{{end}}
{{define "php"}}# Managed by OneinStack Panel - {{.Name}}
server {
    listen {{.ListenPort}};
    server_name {{.ServerNames}};
{{template "challenge" .}}
{{if .ForceHTTPS}}
    location / {
        return 301 https://$host$request_uri;
    }
{{else}}
{{template "php-content" .}}
{{.ServerDirectives}}
{{.ExtraLocations}}
{{end}}
{{template "logs" .}}
}
{{if .TLSEnabled}}
server {
    listen 443 ssl http2;
    server_name {{.ServerNames}};
{{template "tls" .}}
{{template "challenge" .}}
{{template "php-content" .}}
{{.ServerDirectives}}
{{.ExtraLocations}}
{{template "logs" .}}
}
{{end}}
{{end}}
{{define "proxy"}}# Managed by OneinStack Panel - {{.Name}}
server {
    listen {{.ListenPort}};
    server_name {{.ServerNames}};
{{template "challenge" .}}
{{if .ForceHTTPS}}
    location / {
        return 301 https://$host$request_uri;
    }
{{else}}
{{template "proxy-content" .}}
{{.ServerDirectives}}
{{.ExtraLocations}}
{{end}}
{{template "logs" .}}
}
{{if .TLSEnabled}}
server {
    listen 443 ssl http2;
    server_name {{.ServerNames}};
{{template "tls" .}}
{{template "challenge" .}}
{{template "proxy-content" .}}
{{.ServerDirectives}}
{{.ExtraLocations}}
{{template "logs" .}}
}
{{end}}
{{end}}
{{define "static"}}# Managed by OneinStack Panel - {{.Name}}
server {
    listen {{.ListenPort}};
    server_name {{.ServerNames}};
{{template "challenge" .}}
{{if .ForceHTTPS}}
    location / {
        return 301 https://$host$request_uri;
    }
{{else}}
{{template "static-content" .}}
{{.ServerDirectives}}
{{.ExtraLocations}}
{{end}}
{{template "logs" .}}
}
{{if .TLSEnabled}}
server {
    listen 443 ssl http2;
    server_name {{.ServerNames}};
{{template "tls" .}}
{{template "challenge" .}}
{{template "static-content" .}}
{{.ServerDirectives}}
{{.ExtraLocations}}
{{template "logs" .}}
}
{{end}}
{{end}}
`))

type TLSOptions struct {
	Enabled    bool
	ForceHTTPS bool
	CertPath   string
	KeyPath    string
}

type preparedWebsite struct {
	model      models.Website
	config     string
	configName string
	listenPort int
}

func prepareWebsite(input *models.Website, webRoot, logRoot string) (*preparedWebsite, error) {
	return prepareWebsiteWithTLS(input, webRoot, logRoot, defaultChallengeRoot, TLSOptions{})
}

func prepareWebsiteWithTLS(
	input *models.Website,
	webRoot, logRoot, challengeRoot string,
	tlsOptions TLSOptions,
) (*preparedWebsite, error) {
	return prepareWebsiteWithTLSAndSettings(
		input, webRoot, logRoot, challengeRoot, tlsOptions, nil,
	)
}

func prepareWebsiteForCreate(
	input *models.Website,
	webRoot, logRoot, challengeRoot string,
	tlsOptions TLSOptions,
	allowManagedAbsolute bool,
) (*preparedWebsite, error) {
	if input == nil {
		return nil, errors.New("website parameters are required")
	}
	if strings.EqualFold(strings.TrimSpace(input.Type), "proxy") {
		return prepareWebsiteWithTLSAndSettings(input, webRoot, logRoot, challengeRoot, tlsOptions, nil)
	}
	if err := validateWebsiteRootInput(webRoot, input.RootDir, input.Dir, allowManagedAbsolute); err != nil {
		return nil, err
	}
	return prepareWebsiteWithTLSAndSettings(input, webRoot, logRoot, challengeRoot, tlsOptions, nil)
}

func prepareWebsiteWithTLSAndSettings(
	input *models.Website,
	webRoot, logRoot, challengeRoot string,
	tlsOptions TLSOptions,
	settings *models.WebsiteSetting,
) (*preparedWebsite, error) {
	if input == nil {
		return nil, errors.New("website parameters are required")
	}
	siteType := strings.ToLower(strings.TrimSpace(input.Type))
	switch siteType {
	case "php", "proxy", "static":
	default:
		return nil, fmt.Errorf("unsupported website type %q", input.Type)
	}
	domains, port, err := normalizeDomains(input.Domain, input.Name)
	if err != nil {
		return nil, err
	}
	primary := domains[0]
	name := primary
	if strings.HasPrefix(name, "*.") {
		name = strings.TrimPrefix(name, "*.")
	}
	logName := strings.ReplaceAll(name, ".", "_")
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`).MatchString(logName) {
		return nil, errors.New("primary domain cannot be converted to a safe site name")
	}
	configName := name + ".conf"
	if !configNamePattern.MatchString(configName) {
		return nil, errors.New("primary domain cannot be used as a safe Nginx config name")
	}

	webRoot, err = normalizedBasePath(webRoot, "website root")
	if err != nil {
		return nil, err
	}
	logRoot, err = normalizedBasePath(logRoot, "website log root")
	if err != nil {
		return nil, err
	}
	challengeRoot, err = normalizedBasePath(challengeRoot, "ACME challenge root")
	if err != nil {
		return nil, err
	}
	certPath := ""
	keyPath := ""
	if tlsOptions.Enabled {
		certPath, err = normalizeTLSFilePath(tlsOptions.CertPath, "certificate")
		if err != nil {
			return nil, err
		}
		keyPath, err = normalizeTLSFilePath(tlsOptions.KeyPath, "private key")
		if err != nil {
			return nil, err
		}
	}
	if tlsOptions.ForceHTTPS && !tlsOptions.Enabled {
		return nil, errors.New("force HTTPS requires an active certificate")
	}
	rootDir := ""
	dir := ""
	if siteType != "proxy" {
		rootDir, dir, err = normalizeWebsiteRoot(webRoot, input.RootDir, input.Dir, name)
		if err != nil {
			return nil, err
		}
	}
	remark := sanitizeComment(input.Remark)
	proxyURL := ""
	proxyHost := ""
	scheme := ""
	sendURL := ""
	targetHost := ""
	if siteType == "proxy" {
		proxyURL, proxyHost, scheme, sendURL, targetHost, err = normalizeProxy(
			input.Pact,
			input.SendUrl,
			input.TarUrl,
		)
		if err != nil {
			return nil, err
		}
	}

	runtimeSettings, err := renderWebsiteSettings(input, rootDir, settings)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebsiteSettingsValidate, err)
	}
	if runtimeSettings.RootDir != "" {
		rootDir = runtimeSettings.RootDir
	}
	engine, err := normalizeWebsiteEngine(input.Engine)
	if err != nil {
		return nil, err
	}
	data := siteTemplateData{
		Name:              name,
		ListenPort:        port,
		ServerNames:       strings.Join(domains, " "),
		RootDir:           rootDir,
		ProxyURL:          proxyURL,
		ProxyHost:         proxyHost,
		Remark:            remark,
		LogDir:            strings.TrimSuffix(logRoot, string(filepath.Separator)),
		LogName:           logName,
		ChallengeRoot:     strings.TrimSuffix(challengeRoot, string(filepath.Separator)),
		TLSEnabled:        tlsOptions.Enabled,
		ForceHTTPS:        tlsOptions.ForceHTTPS,
		CertPath:          certPath,
		KeyPath:           keyPath,
		DefaultDocuments:  runtimeSettings.DefaultDocuments,
		AutoIndex:         runtimeSettings.AutoIndex,
		PHPBackend:        runtimeSettings.PHPBackend,
		RewriteDirectives: runtimeSettings.RewriteDirectives,
		ServerDirectives:  runtimeSettings.ServerDirectives,
		ExtraLocations:    runtimeSettings.ExtraLocations,
		AccessLogEnabled:  runtimeSettings.AccessLogEnabled,
		ErrorLogEnabled:   runtimeSettings.ErrorLogEnabled,
	}
	rendered, err := renderWebsiteEngineConfig(engine, siteType, data)
	if err != nil {
		return nil, err
	}

	result := *input
	result.Name = name
	result.Domain = strings.Join(domains, ",")
	result.Type = siteType
	result.Engine = engine
	result.RootDir = rootDir
	result.Dir = dir
	result.Remark = remark
	result.Pact = scheme
	result.SendUrl = sendURL
	result.TarUrl = targetHost
	return &preparedWebsite{
		model:      result,
		config:     strings.TrimSpace(rendered) + "\n",
		configName: configName,
		listenPort: port,
	}, nil
}

func normalizeWebsiteEngine(value string) (string, error) {
	engine := strings.ToLower(strings.TrimSpace(value))
	if engine == "" {
		engine = "nginx"
	}
	switch engine {
	case "nginx", "openresty", "tengine", "apache", "caddy":
		return engine, nil
	default:
		return "", fmt.Errorf("unsupported website engine %q", value)
	}
}

// WebServerAdapter contains the engine-specific website lifecycle operations.
// The Nginx-family adapters share the same renderer and command semantics,
// while Apache and Caddy keep their native configuration validation paths.
type WebServerAdapter interface {
	Engine() string
	Render(siteType string, data siteTemplateData) (string, error)
	Validate(context.Context, *Publisher) error
	Reload(context.Context, *Publisher) error
}

type NginxAdapter struct{}
type OpenRestyAdapter struct{}
type TengineAdapter struct{}
type ApacheAdapter struct{}
type CaddyAdapter struct{}

func (NginxAdapter) Engine() string     { return "nginx" }
func (OpenRestyAdapter) Engine() string { return "openresty" }
func (TengineAdapter) Engine() string   { return "tengine" }
func (ApacheAdapter) Engine() string    { return "apache" }
func (CaddyAdapter) Engine() string     { return "caddy" }

func (adapter NginxAdapter) Render(siteType string, data siteTemplateData) (string, error) {
	return renderNginxWebsiteConfig(adapter.Engine(), siteType, data)
}

func (adapter OpenRestyAdapter) Render(siteType string, data siteTemplateData) (string, error) {
	return renderNginxWebsiteConfig(adapter.Engine(), siteType, data)
}

func (adapter TengineAdapter) Render(siteType string, data siteTemplateData) (string, error) {
	return renderNginxWebsiteConfig(adapter.Engine(), siteType, data)
}

func (NginxAdapter) Validate(ctx context.Context, publisher *Publisher) error {
	return validateNginxPublisher(ctx, publisher)
}

func (OpenRestyAdapter) Validate(ctx context.Context, publisher *Publisher) error {
	return validateNginxPublisher(ctx, publisher)
}

func (TengineAdapter) Validate(ctx context.Context, publisher *Publisher) error {
	return validateNginxPublisher(ctx, publisher)
}

func (NginxAdapter) Reload(ctx context.Context, publisher *Publisher) error {
	return reloadNginxPublisher(ctx, publisher)
}

func (OpenRestyAdapter) Reload(ctx context.Context, publisher *Publisher) error {
	return reloadNginxPublisher(ctx, publisher)
}

func (TengineAdapter) Reload(ctx context.Context, publisher *Publisher) error {
	return reloadNginxPublisher(ctx, publisher)
}

func (ApacheAdapter) Render(siteType string, data siteTemplateData) (string, error) {
	if strings.TrimSpace(data.RewriteDirectives) != "" ||
		strings.TrimSpace(data.ServerDirectives) != "" ||
		strings.TrimSpace(data.ExtraLocations) != "" {
		return "", errors.New("UNSUPPORTED_ENGINE_DIRECTIVE: Nginx-specific website directives cannot be rendered for Apache")
	}
	return renderApacheWebsiteConfig(siteType, data), nil
}

func (CaddyAdapter) Render(siteType string, data siteTemplateData) (string, error) {
	if strings.TrimSpace(data.RewriteDirectives) != "" ||
		strings.TrimSpace(data.ExtraLocations) != "" {
		return "", errors.New("UNSUPPORTED_ENGINE_DIRECTIVE: Nginx-specific website directives cannot be rendered for Caddy")
	}
	serverDirectives, err := renderCaddyServerDirectives(data.ServerDirectives)
	if err != nil {
		return "", err
	}
	data.ServerDirectives = serverDirectives
	return renderCaddyWebsiteConfig(siteType, data), nil
}

func (ApacheAdapter) Validate(ctx context.Context, publisher *Publisher) error {
	if publisher == nil || publisher.MainConfigPath == "" {
		return errors.New("Apache main config is not configured")
	}
	return publisher.runCommand(ctx, publisher.NginxBinary, "-t", "-f", publisher.MainConfigPath)
}

func (CaddyAdapter) Validate(ctx context.Context, publisher *Publisher) error {
	if publisher == nil || publisher.MainConfigPath == "" {
		return errors.New("Caddy main config is not configured")
	}
	return publisher.runCommand(ctx, publisher.NginxBinary, "validate", "--config", publisher.MainConfigPath, "--adapter", "caddyfile")
}

func (ApacheAdapter) Reload(ctx context.Context, publisher *Publisher) error {
	return reloadSystemdPublisher(ctx, publisher)
}

func (CaddyAdapter) Reload(ctx context.Context, publisher *Publisher) error {
	return reloadCaddyPublisher(ctx, publisher)
}

func webServerAdapterForEngine(engine string) (WebServerAdapter, error) {
	normalized, err := normalizeWebsiteEngine(engine)
	if err != nil {
		return nil, err
	}
	switch normalized {
	case "nginx":
		return NginxAdapter{}, nil
	case "openresty":
		return OpenRestyAdapter{}, nil
	case "tengine":
		return TengineAdapter{}, nil
	case "apache":
		return ApacheAdapter{}, nil
	case "caddy":
		return CaddyAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported website engine %q", engine)
	}
}

func renderWebsiteEngineConfig(engine, siteType string, data siteTemplateData) (string, error) {
	adapter, err := webServerAdapterForEngine(engine)
	if err != nil {
		return "", err
	}
	return adapter.Render(siteType, data)
}

func renderNginxWebsiteConfig(engine, siteType string, data siteTemplateData) (string, error) {
	var rendered bytes.Buffer
	if err := siteTemplates.ExecuteTemplate(&rendered, siteType, data); err != nil {
		return "", fmt.Errorf("render %s website config: %w", engine, err)
	}
	return rendered.String(), nil
}

func renderApacheWebsiteConfig(siteType string, data siteTemplateData) string {
	serverName := strings.Fields(data.ServerNames)
	primary := "_"
	if len(serverName) > 0 {
		primary = serverName[0]
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Managed by OneinStack Panel - %s\n<VirtualHost *:%d>\n  ServerName %s\n", data.Name, data.ListenPort, primary)
	for _, alias := range serverName[1:] {
		fmt.Fprintf(&builder, "  ServerAlias %s\n", alias)
	}
	if siteType == "proxy" {
		fmt.Fprintf(&builder, "  ProxyPreserveHost On\n  ProxyPass / %s\n  ProxyPassReverse / %s\n", data.ProxyURL, data.ProxyURL)
	} else {
		fmt.Fprintf(&builder, "  DocumentRoot %s\n  <Directory %s>\n    AllowOverride All\n    Require all granted\n  </Directory>\n", data.RootDir, data.RootDir)
		if siteType == "php" {
			backend := strings.TrimPrefix(data.PHPBackend, "unix:")
			fmt.Fprintf(&builder, "  <FilesMatch [.]php$>\n    SetHandler \"proxy:unix:%s|fcgi://localhost/\"\n  </FilesMatch>\n", backend)
		}
	}
	if data.TLSEnabled {
		fmt.Fprintf(&builder, "  SSLEngine on\n  SSLCertificateFile %s\n  SSLCertificateKeyFile %s\n", data.CertPath, data.KeyPath)
	}
	fmt.Fprintf(&builder, "  ErrorLog %s/%s_error.log\n  CustomLog %s/%s_access.log combined\n</VirtualHost>\n", data.LogDir, data.LogName, data.LogDir, data.LogName)
	return builder.String()
}

func renderCaddyWebsiteConfig(siteType string, data siteTemplateData) string {
	addresses := strings.Fields(data.ServerNames)
	for index, address := range addresses {
		if data.ListenPort != 80 {
			address = fmt.Sprintf("%s:%d", address, data.ListenPort)
		}
		// A site without a certificate must be explicitly HTTP-only. Otherwise
		// Caddy enables automatic HTTPS and starts an ACME order during reload.
		if !data.TLSEnabled {
			address = "http://" + address
		}
		addresses[index] = address
	}
	address := strings.Join(addresses, " ")
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Managed by OneinStack Panel - %s\n%s {\n", data.Name, address)
	if data.TLSEnabled {
		fmt.Fprintf(&builder, "  tls %s %s\n", data.CertPath, data.KeyPath)
	}
	if siteType == "proxy" {
		fmt.Fprintf(&builder, "  reverse_proxy %s\n", data.ProxyURL)
	} else {
		fmt.Fprintf(&builder, "  root * %s\n", data.RootDir)
		if siteType == "php" {
			backend := strings.TrimPrefix(data.PHPBackend, "unix:")
			fmt.Fprintf(&builder, "  php_fastcgi unix//%s\n", strings.TrimPrefix(backend, "/"))
		}
		builder.WriteString("  file_server\n")
	}
	if directives := data.ServerDirectives; strings.TrimSpace(directives) != "" {
		builder.WriteString(directives)
		builder.WriteByte('\n')
	}
	fmt.Fprintf(&builder, "  log {\n    output file %s/%s_access.log\n  }\n}\n", data.LogDir, data.LogName)
	return builder.String()
}

// renderCaddyServerDirectives translates the one server-level setting that
// has a direct Caddy equivalent. Other values remain explicitly unsupported
// instead of being silently dropped from the generated configuration.
func renderCaddyServerDirectives(value string) (string, error) {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}
	normalized := strings.TrimSpace(strings.Join(lines, "\n"))
	if normalized == "" {
		return "", nil
	}
	if normalized != strings.Join([]string{
		`add_header X-Content-Type-Options "nosniff" always;`,
		`add_header X-Frame-Options "SAMEORIGIN" always;`,
		`add_header Referrer-Policy "strict-origin-when-cross-origin" always;`,
	}, "\n") {
		return "", errors.New("UNSUPPORTED_ENGINE_DIRECTIVE: Nginx-specific website directives cannot be rendered for Caddy")
	}
	return strings.Join([]string{
		"  header {",
		"    X-Content-Type-Options \"nosniff\"",
		"    X-Frame-Options \"SAMEORIGIN\"",
		"    Referrer-Policy \"strict-origin-when-cross-origin\"",
		"  }",
	}, "\n"), nil
}

func normalizeTLSFilePath(value, label string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
		return "", fmt.Errorf("%s path must be a non-root absolute path", label)
	}
	if strings.ContainsAny(cleaned, "\r\n\t ;{}\"'$") {
		return "", fmt.Errorf("%s path contains characters unsafe for Nginx configuration", label)
	}
	return cleaned, nil
}

// GetNginxConfig validates and renders a site using the configured model paths.
// Persistence and reload are deliberately handled by Publisher.
func GetNginxConfig(site *models.Website) (string, error) {
	prepared, err := prepareWebsite(site, "/data/wwwroot", "/data/wwwlogs")
	if err != nil {
		return "", err
	}
	return prepared.config, nil
}

func normalizeDomains(domainValue, fallbackName string) ([]string, int, error) {
	value := strings.TrimSpace(domainValue)
	if value == "" {
		value = strings.TrimSpace(fallbackName)
	}
	tokens := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(tokens) == 0 || len(tokens) > 100 {
		return nil, 0, errors.New("between 1 and 100 website domains are required")
	}
	domains := make([]string, 0, len(tokens))
	seen := make(map[string]struct{})
	listenPort := 80
	explicitPort := 0
	for _, token := range tokens {
		host, port, err := normalizeDomainToken(token)
		if err != nil {
			return nil, 0, err
		}
		if port > 0 {
			if explicitPort != 0 && explicitPort != port {
				return nil, 0, errors.New("all website domains must use the same listen port")
			}
			explicitPort = port
			listenPort = port
		}
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		domains = append(domains, host)
	}
	if len(domains) == 0 {
		return nil, 0, errors.New("at least one unique website domain is required")
	}
	return domains, listenPort, nil
}

func normalizeDomainToken(value string) (string, int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.ContainsAny(value, `/\;"'{}()`) {
		return "", 0, fmt.Errorf("invalid website domain %q", value)
	}
	host := value
	port := 0
	if strings.Count(value, ":") == 1 {
		var portValue string
		host, portValue, _ = strings.Cut(value, ":")
		parsed, err := strconv.Atoi(portValue)
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", 0, fmt.Errorf("invalid website port in %q", value)
		}
		port = parsed
	}
	wildcard := strings.HasPrefix(host, "*.")
	plainHost := strings.TrimPrefix(host, "*.")
	if address := net.ParseIP(plainHost); address == nil {
		labels := strings.Split(plainHost, ".")
		if len(labels) < 2 {
			return "", 0, fmt.Errorf("website domain %q must be a fully qualified domain or IP", value)
		}
		for _, label := range labels {
			if !domainLabelPattern.MatchString(label) {
				return "", 0, fmt.Errorf("invalid website domain %q", value)
			}
		}
	}
	if wildcard {
		host = "*." + plainHost
	} else {
		host = plainHost
	}
	return host, port, nil
}

func normalizedBasePath(value, label string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) || value == string(filepath.Separator) {
		return "", fmt.Errorf("%s must be a non-root absolute path", label)
	}
	if strings.ContainsAny(value, "\r\n\t ;{}\"'$") {
		return "", fmt.Errorf("%s contains characters unsafe for Nginx configuration", label)
	}
	return value + string(filepath.Separator), nil
}

func normalizeWebsiteRoot(webRoot, rootValue, dirValue, defaultDir string) (string, string, error) {
	value := strings.TrimSpace(rootValue)
	if value == "" {
		value = strings.TrimSpace(dirValue)
	}
	if value == "" {
		value = defaultDir
	}
	if strings.ContainsAny(value, "\r\n\t ;{}\"'$") {
		return "", "", errors.New("website root contains characters unsafe for Nginx configuration")
	}
	base := filepath.Clean(webRoot)
	if filepath.IsAbs(value) {
		cleaned := filepath.Clean(value)
		if relative, err := filepath.Rel(base, cleaned); err == nil &&
			relative != "." &&
			relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			value = relative
		} else {
			value = strings.TrimPrefix(cleaned, string(filepath.Separator))
		}
	}
	value = filepath.Clean(value)
	if value == "." || value == ".." || filepath.IsAbs(value) ||
		strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return "", "", errors.New("website root must be a relative path below the configured web root")
	}
	root := filepath.Join(base, value)
	relative, err := filepath.Rel(base, root)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("website root escapes the configured web root")
	}
	return root, filepath.ToSlash(relative), nil
}

func validateWebsiteRootInput(webRoot, rootValue, dirValue string, allowManagedAbsolute bool) error {
	value := strings.TrimSpace(rootValue)
	if value == "" {
		value = strings.TrimSpace(dirValue)
	}
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\r\n\t ;{}\"'\\$") {
		return fmt.Errorf("%w: website root contains unsafe characters", ErrWebsiteRootInvalid)
	}
	cleaned := filepath.Clean(value)
	if filepath.IsAbs(cleaned) {
		if !allowManagedAbsolute {
			// Keep compatibility with the historical "/site-name" shorthand.
			// normalizeWebsiteRoot treats this form as a path relative to webRoot;
			// the preflight validation must apply the same interpretation.
			value = strings.TrimPrefix(cleaned, string(filepath.Separator))
			cleaned = filepath.Clean(value)
			if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) ||
				strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return fmt.Errorf("%w: website root must be a relative directory below the configured web root", ErrWebsiteRootInvalid)
			}
			return nil
		}
		base := filepath.Clean(webRoot)
		relative, err := filepath.Rel(base, cleaned)
		if err == nil && relative != "." && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil
		}
		// Match normalizeWebsiteRoot's legacy "/site-name" shorthand when
		// the absolute value is not already inside the configured root.
		value = strings.TrimPrefix(cleaned, string(filepath.Separator))
		cleaned = filepath.Clean(value)
		if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) ||
			strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: website root must be strictly below the configured web root", ErrWebsiteRootInvalid)
		}
		return nil
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: website root must be a relative directory below the configured web root", ErrWebsiteRootInvalid)
	}
	return nil
}

func normalizeProxy(protocol, sendValue, hostHeader string) (string, string, string, string, string, error) {
	scheme := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(protocol), "://"))
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return "", "", "", "", "", errors.New("proxy protocol must be http or https")
	}
	sendValue = strings.TrimSpace(sendValue)
	if sendValue == "" || strings.ContainsAny(sendValue, "\r\n\t ;{}\"'\\$") {
		return "", "", "", "", "", errors.New("proxy target is required and cannot contain whitespace")
	}
	target, err := url.Parse(scheme + "://" + sendValue)
	if err != nil || target.Host == "" || target.User != nil || target.Fragment != "" {
		return "", "", "", "", "", errors.New("proxy target must be a host, optional port, and optional path")
	}
	if target.Scheme != scheme {
		return "", "", "", "", "", errors.New("proxy target protocol is inconsistent")
	}
	proxyHost := strings.TrimSpace(hostHeader)
	if proxyHost == "" {
		proxyHost = "$host"
	}
	if proxyHost != "$host" && proxyHost != "$http_host" {
		if strings.ContainsAny(proxyHost, "\r\n\t ;$") {
			return "", "", "", "", "", errors.New("proxy Host header contains unsafe characters")
		}
		if _, _, err := normalizeDomainToken(proxyHost); err != nil {
			if net.ParseIP(proxyHost) == nil {
				return "", "", "", "", "", errors.New("proxy Host header must be a hostname, IP, $host, or $http_host")
			}
		}
	}
	return target.String(), proxyHost, scheme, sendValue, proxyHost, nil
}

func sanitizeComment(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 200 {
		value = string(runes[:200])
	}
	if value == "" {
		return "无"
	}
	return value
}

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

type Publisher struct {
	ConfigDir      string
	NginxBinary    string
	Engine         string
	ServiceName    string
	MainConfigPath string
	Runner         CommandRunner
}

type configSnapshot struct {
	name   string
	exists bool
	data   []byte
	mode   os.FileMode
}

type Publication struct {
	publisher *Publisher
	snapshots []configSnapshot
	once      sync.Once
	err       error
}

// Publish atomically replaces the requested config files, validates the whole
// target-engine configuration, and reloads only after validation succeeds. A
// nil value means delete the named config.
func (p *Publisher) Publish(ctx context.Context, changes map[string]*string) (*Publication, error) {
	if p == nil || p.Runner == nil {
		return nil, errors.New("Web server publisher is not configured")
	}
	if len(changes) == 0 {
		return nil, errors.New("Web server publication has no changes")
	}
	configDir := filepath.Clean(strings.TrimSpace(p.ConfigDir))
	if !filepath.IsAbs(configDir) || configDir == string(filepath.Separator) {
		return nil, errors.New("Web server config directory must be a non-root absolute path")
	}
	if strings.TrimSpace(p.NginxBinary) == "" {
		return nil, errors.New("Web server binary is not configured")
	}
	if err := os.MkdirAll(configDir, 0750); err != nil {
		return nil, fmt.Errorf("create Web server config directory: %w", err)
	}
	if strings.EqualFold(p.Engine, "caddy") {
		if err := ensureCaddyManagedConfigAccess(configDir, p.MainConfigPath); err != nil {
			return nil, fmt.Errorf("prepare Caddy runtime config access: %w", err)
		}
	}

	names := make([]string, 0, len(changes))
	for name := range changes {
		if !configNamePattern.MatchString(name) || filepath.Base(name) != name {
			return nil, fmt.Errorf("unsafe Web server config name %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	publication := &Publication{publisher: p}
	changedPaths := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(configDir, name)
		changedPaths = append(changedPaths, path)
		snapshot, err := snapshotConfig(name, path)
		if err != nil {
			if len(publication.snapshots) > 0 {
				_ = restorePublication(publication)
			}
			return nil, err
		}
		publication.snapshots = append(publication.snapshots, snapshot)
		if content := changes[name]; content != nil {
			if err := atomicWriteConfig(configDir, path, []byte(*content)); err != nil {
				_ = restorePublication(publication)
				return nil, err
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = restorePublication(publication)
			return nil, fmt.Errorf("delete Web server config %s: %w", name, err)
		}
	}
	if strings.EqualFold(p.Engine, "caddy") {
		if err := ensureCaddyManagedConfigAccess(configDir, p.MainConfigPath, changedPaths...); err != nil {
			restoreErr := restorePublication(publication)
			if restoreErr != nil {
				return nil, fmt.Errorf("prepare Caddy runtime config access failed: %w; restore failed: %v", err, restoreErr)
			}
			return nil, fmt.Errorf("prepare Caddy runtime config access failed; previous configuration restored: %w", err)
		}
	}

	if err := p.runEngine(ctx, "validate"); err != nil {
		restoreErr := restorePublication(publication)
		if restoreErr != nil {
			return nil, fmt.Errorf("Web server config validation failed: %w; restore failed: %v", err, restoreErr)
		}
		return nil, fmt.Errorf("Web server config validation failed; previous configuration restored: %w", err)
	}
	if err := p.runEngine(ctx, "reload"); err != nil {
		restoreErr := restorePublication(publication)
		if restoreErr != nil {
			return nil, fmt.Errorf("Web server reload failed: %w; restore/reload failed: %v", err, restoreErr)
		}
		return nil, fmt.Errorf("Web server reload failed; previous configuration restored and reloaded: %w", err)
	}
	return publication, nil
}

func (publication *Publication) Rollback(ctx context.Context) error {
	if publication == nil {
		return nil
	}
	publication.once.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		rollbackCtx, cancel := context.WithTimeout(ctx, publishRollbackTimeout)
		defer cancel()
		publication.err = publication.restore(rollbackCtx)
	})
	return publication.err
}

func restorePublication(publication *Publication) error {
	if publication == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishRollbackTimeout)
	defer cancel()
	return publication.restore(ctx)
}

func (publication *Publication) restore(ctx context.Context) error {
	if publication == nil || publication.publisher == nil {
		return nil
	}
	for _, snapshot := range publication.snapshots {
		path := filepath.Join(publication.publisher.ConfigDir, snapshot.name)
		if snapshot.exists {
			if err := atomicWriteConfig(
				publication.publisher.ConfigDir,
				path,
				snapshot.data,
			); err != nil {
				return err
			}
			if err := os.Chmod(path, snapshot.mode.Perm()); err != nil {
				return fmt.Errorf("restore Web server config permissions: %w", err)
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove newly published Web server config: %w", err)
		}
	}
	if strings.EqualFold(publication.publisher.Engine, "caddy") {
		if err := ensureCaddyManagedConfigAccess(
			publication.publisher.ConfigDir,
			publication.publisher.MainConfigPath,
		); err != nil {
			return fmt.Errorf("restore Caddy runtime config access: %w", err)
		}
	}
	if err := publication.publisher.runEngine(ctx, "validate"); err != nil {
		return fmt.Errorf("restored Web server config validation failed: %w", err)
	}
	if err := publication.publisher.runEngine(ctx, "reload"); err != nil {
		return fmt.Errorf("reload restored Web server config: %w", err)
	}
	return nil
}

func snapshotConfig(name, path string) (configSnapshot, error) {
	snapshot := configSnapshot{name: name, mode: 0640}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, fmt.Errorf("stat Web server config %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return snapshot, fmt.Errorf("Web server config %s is not a regular file", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshot, fmt.Errorf("read Web server config %s: %w", name, err)
	}
	snapshot.exists = true
	snapshot.data = data
	snapshot.mode = info.Mode()
	return snapshot, nil
}

func atomicWriteConfig(directory, target string, data []byte) error {
	temporary, err := os.CreateTemp(directory, ".oneinstack-web-server-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Web server config: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0640); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary Web server config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary Web server config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary Web server config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Web server config: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("publish Web server config: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

const caddyRuntimeGroupName = "caddy"

// ensureCaddyManagedConfigAccess keeps Caddy-managed configuration readable
// by the non-root user used by the OneinStack Caddy unit. The Panel runs as
// root and atomic renames otherwise recreate files as root:root, which makes
// a later systemd reload fail even though root-level validation succeeds.
func ensureCaddyManagedConfigAccess(configDir, mainConfigPath string, paths ...string) error {
	configDir = filepath.Clean(strings.TrimSpace(configDir))
	if configDir == "." || !filepath.IsAbs(configDir) || configDir == string(filepath.Separator) {
		return errors.New("Caddy managed config directory is invalid")
	}
	group, err := osuser.LookupGroup(caddyRuntimeGroupName)
	if err != nil {
		return fmt.Errorf("lookup Caddy runtime group: %w", err)
	}
	gid, err := strconv.Atoi(strings.TrimSpace(group.Gid))
	if err != nil || gid < 0 {
		return fmt.Errorf("Caddy runtime group ID is invalid")
	}

	if err := ensureCaddyConfigDirectory(configDir, gid); err != nil {
		return fmt.Errorf("prepare Caddy managed config directory: %w", err)
	}
	if mainConfigPath != "" {
		mainConfigPath = filepath.Clean(mainConfigPath)
		if err := ensureCaddyConfigTraversal(filepath.Dir(mainConfigPath), gid); err != nil {
			return fmt.Errorf("prepare Caddy main config path: %w", err)
		}
		if err := ensureCaddyConfigFile(mainConfigPath, gid); err != nil {
			return fmt.Errorf("prepare Caddy main config: %w", err)
		}
	}
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || !filepath.IsAbs(path) || path == string(filepath.Separator) {
			continue
		}
		if err := ensureCaddyConfigDirectory(filepath.Dir(path), gid); err != nil {
			return fmt.Errorf("prepare Caddy config path %s: %w", filepath.Base(path), err)
		}
		if err := ensureCaddyConfigFile(path, gid); err != nil {
			return fmt.Errorf("prepare Caddy config %s: %w", filepath.Base(path), err)
		}
	}

	entries, err := os.ReadDir(configDir)
	if err != nil {
		return fmt.Errorf("inspect Caddy managed config directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".conf") {
			continue
		}
		path := filepath.Join(configDir, entry.Name())
		if err := ensureCaddyConfigFile(path, gid); err != nil {
			return fmt.Errorf("prepare Caddy managed config %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func ensureCaddyConfigDirectory(path string, gid int) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return errors.New("Caddy config directory must be a non-root absolute path")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s is not a real directory", current)
		}
		isLeaf := current == path
		if isLeaf || info.Mode().Perm()&0001 == 0 {
			if err := os.Chown(current, -1, gid); err != nil {
				return err
			}
			required := os.FileMode(0010)
			if isLeaf {
				required = 0050
			}
			if err := os.Chmod(current, info.Mode().Perm()|required); err != nil {
				return err
			}
		}
		parent := filepath.Dir(current)
		if parent == current || current == string(filepath.Separator) {
			break
		}
	}
	return nil
}

func ensureCaddyConfigTraversal(path string, gid int) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return errors.New("Caddy config parent must be a non-root absolute path")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s is not a real directory", current)
		}
		if info.Mode().Perm()&0001 == 0 {
			if err := os.Chown(current, -1, gid); err != nil {
				return err
			}
			if err := os.Chmod(current, info.Mode().Perm()|0010); err != nil {
				return err
			}
		}
		parent := filepath.Dir(current)
		if parent == current || current == string(filepath.Separator) {
			break
		}
	}
	return nil
}

func ensureCaddyConfigFile(path string, gid int) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0640)
}

func (p *Publisher) runNginx(ctx context.Context, args ...string) error {
	output, err := p.Runner.Run(ctx, p.NginxBinary, args...)
	return commandErrorForContext(ctx, output, err)
}

func (p *Publisher) runEngine(ctx context.Context, operation string) error {
	// Bound each engine command so a stuck caddy validate or a blocked
	// systemctl reload cannot hold locks indefinitely. Matches the timeout
	// used by the configuration preview path in webserver.go.
	if ctx == nil {
		ctx = context.Background()
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, publishCommandTimeout)
	defer cancel()
	adapter, err := webServerAdapterForEngine(p.Engine)
	if err != nil {
		return err
	}
	switch operation {
	case "validate":
		return adapter.Validate(timeoutCtx, p)
	case "reload":
		return adapter.Reload(timeoutCtx, p)
	default:
		return fmt.Errorf("unsupported Web server operation %q for %s", operation, adapter.Engine())
	}
}

func validateNginxPublisher(ctx context.Context, publisher *Publisher) error {
	return publisher.runNginx(ctx, "-t")
}

func reloadNginxPublisher(ctx context.Context, publisher *Publisher) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if publisher.ServiceName != "" && !publisher.serviceActive(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	return publisher.runNginx(ctx, "-s", "reload")
}

func reloadSystemdPublisher(ctx context.Context, publisher *Publisher) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if publisher.ServiceName == "" || !publisher.serviceActive(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	return publisher.runCommand(ctx, "systemctl", "reload", publisher.ServiceName+".service")
}

func reloadCaddyPublisher(ctx context.Context, publisher *Publisher) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if publisher == nil {
		return errors.New("Caddy publisher is not configured")
	}
	if publisher.ServiceName == "" {
		return nil
	}
	if strings.TrimSpace(publisher.MainConfigPath) == "" {
		return errors.New("Caddy main config is not configured")
	}

	select {
	case caddyReloadSlot <- struct{}{}:
		defer func() { <-caddyReloadSlot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if !publisher.serviceActive(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	// Invoke Caddy directly instead of `systemctl reload`. The latter can
	// leave a long-running ExecReload job behind after the HTTP request times
	// out, so later operations contend with stale systemd reload jobs.
	return publisher.runCommand(
		ctx,
		publisher.NginxBinary,
		"reload",
		"--config",
		publisher.MainConfigPath,
		"--adapter",
		"caddyfile",
		"--force",
	)
}

func (p *Publisher) runCommand(ctx context.Context, command string, args ...string) error {
	output, err := p.Runner.Run(ctx, command, args...)
	return commandErrorForContext(ctx, output, err)
}

func (p *Publisher) serviceActive(ctx context.Context) bool {
	if strings.TrimSpace(p.ServiceName) == "" {
		return true
	}
	output, err := p.Runner.Run(ctx, "systemctl", "is-active", "--quiet", p.ServiceName+".service")
	return err == nil && strings.TrimSpace(string(output)) == ""
}

func commandError(output []byte, err error) error {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if len(message) > 1000 {
		message = message[:1000]
	}
	if message == "" {
		message = err.Error()
	}
	return errors.New(message)
}

func commandErrorForContext(ctx context.Context, output []byte, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return commandError(output, err)
}
