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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"oneinstack/internal/models"
)

const (
	defaultNginxConfigDir = "/usr/local/nginx/conf/conf.d"
	defaultNginxBinary    = "/usr/local/nginx/sbin/nginx"
	defaultChallengeRoot  = "/usr/local/one/acme-webroot"
)

var (
	ErrWebServerUnavailable    = errors.New("supported web server unavailable")
	ErrWebServerConfigConflict = errors.New("web server configuration revision conflict")
	ErrNginxUnavailable        = ErrWebServerUnavailable
	domainLabelPattern         = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	configNamePattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,252}\.conf$`)
)

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
		return nil, err
	}
	if runtimeSettings.RootDir != "" {
		rootDir = runtimeSettings.RootDir
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
	var rendered bytes.Buffer
	if err := siteTemplates.ExecuteTemplate(&rendered, siteType, data); err != nil {
		return nil, fmt.Errorf("render Nginx website config: %w", err)
	}

	result := *input
	result.Name = name
	result.Domain = strings.Join(domains, ",")
	result.Type = siteType
	result.RootDir = rootDir
	result.Dir = dir
	result.Remark = remark
	result.Pact = scheme
	result.SendUrl = sendURL
	result.TarUrl = targetHost
	return &preparedWebsite{
		model:      result,
		config:     strings.TrimSpace(rendered.String()) + "\n",
		configName: configName,
		listenPort: port,
	}, nil
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
	ConfigDir   string
	NginxBinary string
	Runner      CommandRunner
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
// Nginx configuration, and reloads only after validation succeeds. A nil value
// means delete the named config.
func (p *Publisher) Publish(ctx context.Context, changes map[string]*string) (*Publication, error) {
	if p == nil || p.Runner == nil {
		return nil, errors.New("Nginx publisher is not configured")
	}
	if len(changes) == 0 {
		return nil, errors.New("Nginx publication has no changes")
	}
	configDir := filepath.Clean(strings.TrimSpace(p.ConfigDir))
	if !filepath.IsAbs(configDir) || configDir == string(filepath.Separator) {
		return nil, errors.New("Nginx config directory must be a non-root absolute path")
	}
	if strings.TrimSpace(p.NginxBinary) == "" {
		return nil, errors.New("Nginx binary is not configured")
	}
	if err := os.MkdirAll(configDir, 0750); err != nil {
		return nil, fmt.Errorf("create Nginx config directory: %w", err)
	}

	names := make([]string, 0, len(changes))
	for name := range changes {
		if !configNamePattern.MatchString(name) || filepath.Base(name) != name {
			return nil, fmt.Errorf("unsafe Nginx config name %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	publication := &Publication{publisher: p}
	for _, name := range names {
		path := filepath.Join(configDir, name)
		snapshot, err := snapshotConfig(name, path)
		if err != nil {
			if len(publication.snapshots) > 0 {
				_ = publication.restore(context.Background())
			}
			return nil, err
		}
		publication.snapshots = append(publication.snapshots, snapshot)
		if content := changes[name]; content != nil {
			if err := atomicWriteConfig(configDir, path, []byte(*content)); err != nil {
				_ = publication.restore(context.Background())
				return nil, err
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = publication.restore(context.Background())
			return nil, fmt.Errorf("delete Nginx config %s: %w", name, err)
		}
	}

	if err := p.runNginx(ctx, "-t"); err != nil {
		restoreErr := publication.restore(context.Background())
		if restoreErr != nil {
			return nil, fmt.Errorf("Nginx config test failed: %w; restore failed: %v", err, restoreErr)
		}
		return nil, fmt.Errorf("Nginx config test failed; previous configuration restored: %w", err)
	}
	if err := p.runNginx(ctx, "-s", "reload"); err != nil {
		restoreErr := publication.restore(context.Background())
		if restoreErr != nil {
			return nil, fmt.Errorf("Nginx reload failed: %w; restore/reload failed: %v", err, restoreErr)
		}
		return nil, fmt.Errorf("Nginx reload failed; previous configuration restored and reloaded: %w", err)
	}
	return publication, nil
}

func (publication *Publication) Rollback(ctx context.Context) error {
	if publication == nil {
		return nil
	}
	publication.once.Do(func() {
		publication.err = publication.restore(ctx)
	})
	return publication.err
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
				return fmt.Errorf("restore Nginx config permissions: %w", err)
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove newly published Nginx config: %w", err)
		}
	}
	if err := publication.publisher.runNginx(ctx, "-t"); err != nil {
		return fmt.Errorf("restored Nginx config test failed: %w", err)
	}
	if err := publication.publisher.runNginx(ctx, "-s", "reload"); err != nil {
		return fmt.Errorf("reload restored Nginx config: %w", err)
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
		return snapshot, fmt.Errorf("stat Nginx config %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return snapshot, fmt.Errorf("Nginx config %s is not a regular file", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshot, fmt.Errorf("read Nginx config %s: %w", name, err)
	}
	snapshot.exists = true
	snapshot.data = data
	snapshot.mode = info.Mode()
	return snapshot, nil
}

func atomicWriteConfig(directory, target string, data []byte) error {
	temporary, err := os.CreateTemp(directory, ".oneinstack-nginx-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Nginx config: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0640); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary Nginx config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary Nginx config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary Nginx config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Nginx config: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("publish Nginx config: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func (p *Publisher) runNginx(ctx context.Context, args ...string) error {
	output, err := p.Runner.Run(ctx, p.NginxBinary, args...)
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
