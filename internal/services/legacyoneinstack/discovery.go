// Package legacyoneinstack discovers and binds existing script installations.
// Discovery never sources options.conf, runs an installer, or changes the host.
package legacyoneinstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"oneinstack/internal/models"
)

const Origin = models.SoftwareOriginOneinstackScript
const TaskComponent = "oneinstack-legacy"
const maxFileBytes = 1 << 20

type Component struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Key          string   `json:"key"`
	Version      string   `json:"version"`
	Binary       string   `json:"binary"`
	InstallDir   string   `json:"installDir"`
	DataDir      string   `json:"dataDir,omitempty"`
	Port         string   `json:"port,omitempty"`
	Config       string   `json:"config,omitempty"`
	Service      string   `json:"service,omitempty"`
	UnitIdentity string   `json:"unitIdentity,omitempty"`
	ActiveState  string   `json:"activeState"`
	CanReload    bool     `json:"canReload"`
	WebRoot      string   `json:"webRoot,omitempty"`
	LogRoot      string   `json:"logRoot,omitempty"`
	VhostDir     string   `json:"vhostDir,omitempty"`
	RuntimeGroup string   `json:"runtimeGroup,omitempty"`
	Scopes       []string `json:"scopes"`
}

type Site struct {
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Domains     string `json:"domains"`
	Root        string `json:"root,omitempty"`
	Type        string `json:"type"`
	Config      string `json:"config"`
	AccessLog   string `json:"accessLog,omitempty"`
	ErrorLog    string `json:"errorLog,omitempty"`
	Certificate string `json:"certificate,omitempty"`
	PHPBackend  string `json:"phpBackend,omitempty"`
	Proxy       string `json:"proxy,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

type Report struct {
	ImportedComponents int         `json:"-"`
	ImportedSites      int         `json:"-"`
	SourceDir          string      `json:"sourceDir,omitempty"`
	System             string      `json:"system"`
	SystemVersion      string      `json:"systemVersion,omitempty"`
	Architecture       string      `json:"architecture"`
	Revision           string      `json:"revision"`
	Components         []Component `json:"components"`
	Sites              []Site      `json:"sites"`
	Warnings           []string    `json:"warnings"`
	options            map[string]string
}

var versionPattern = regexp.MustCompile(`\b[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:[-+][0-9A-Za-z._-]+)?`)
var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._@-]{0,126}$`)
var assignment = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)=(.*)$`)

// ReadFile rejects symlinks in the whole path and bounds reads. The scanner
// only accepts static paths, never shell substitutions or arbitrary commands.
func ReadFile(path string) ([]byte, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, errors.New("not a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if len(data) > maxFileBytes {
		return nil, errors.New("file is too large")
	}
	return data, err
}

func ValidatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n$`") {
		return errors.New("unsafe legacy path")
	}
	switch path {
	case "/", "/etc", "/usr", "/usr/local", "/data", "/var", "/root", "/home", "/dev", "/proc", "/sys":
		return errors.New("legacy path is too broad")
	}
	for _, root := range []string{"/proc/", "/sys/", "/dev/"} {
		if strings.HasPrefix(path, root) {
			return errors.New("legacy path is protected")
		}
	}
	for current := path; current != "/"; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("legacy path contains a symbolic link")
		}
	}
	return nil
}

func staticValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
		value = value[1 : len(value)-1]
	} else if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return value, !strings.ContainsAny(value, "\x00\r\n$`;|&<>()\\")
}

func readOptions(path string) (map[string]string, error) {
	data, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		match := assignment.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 3 {
			continue
		}
		key := match[1]
		if !(strings.HasSuffix(key, "_install_dir") || strings.HasSuffix(key, "_data_dir") || key == "wwwroot_dir" || key == "wwwlogs_dir" || key == "dbrootpwd") {
			continue
		}
		if value, ok := staticValue(match[2]); ok {
			values[key] = value
		}
	}
	// Old script releases store the root password here. Only accept it from
	// a root-owned file that cannot be changed by other users. Historical
	// script releases used mode 0644; reading it does not relax its permissions.
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0022 != 0 {
		delete(values, "dbrootpwd")
	} else if owner, ok := info.Sys().(*syscall.Stat_t); !ok || owner.Uid != 0 {
		delete(values, "dbrootpwd")
	}
	return values, nil
}

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var output limitedBuffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return output.data, err
}

type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data) < maxFileBytes {
		remaining := maxFileBytes - len(b.data)
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func unit(ctx context.Context, name string) (map[string]string, error) {
	if !safeName.MatchString(name) {
		return nil, errors.New("invalid service identity")
	}
	args := []string{"show", name + ".service"}
	// Repeated property flags also work on the older systemd releases used
	// by historical OneinStack hosts; no --value or comma-list support needed.
	for _, property := range []string{"Id", "LoadState", "ActiveState", "SubState", "UnitFileState", "CanReload", "ExecStart", "FragmentPath", "SourcePath"} {
		args = append(args, "--property="+property)
	}
	data, err := command(ctx, "systemctl", args...)
	if err != nil {
		return nil, errors.New("legacy service probe failed")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["LoadState"] != "loaded" {
		return nil, errors.New("legacy service is not loaded")
	}
	return values, nil
}

func unitIdentity(values map[string]string) string {
	identity := values["Id"] + "\n" + values["ExecStart"] + "\n" + values["FragmentPath"] + "\n" + values["SourcePath"]
	for _, path := range []string{values["FragmentPath"], values["SourcePath"]} {
		if path != "" {
			if data, err := ReadFile(path); err == nil {
				identity += "\n" + digest(data)
			}
		}
	}
	return digest([]byte(identity))
}

func digest(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }

func Scan(ctx context.Context, sourceDir string) (*Report, error) {
	r := &Report{System: runtime.GOOS, Architecture: runtime.GOARCH, Components: []Component{}, Sites: []Site{}, Warnings: []string{}, options: map[string]string{}}
	if runtime.GOOS != "linux" {
		return r, nil
	}
	if sourceDir == "" {
		sourceDir = strings.TrimSpace(os.Getenv("ONEINSTACK_LEGACY_ROOT"))
	}
	osRelease, _ := filepath.EvalSymlinks("/etc/os-release")
	if data, err := ReadFile(osRelease); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "ID=") {
				r.System = strings.Trim(strings.TrimPrefix(line, "ID="), "\"'")
			}
			if strings.HasPrefix(line, "VERSION_ID=") {
				r.SystemVersion = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), "\"'")
			}
		}
	}
	roots := []string{"/root/oneinstack", "/opt/oneinstack", "/usr/local/oneinstack"}
	if sourceDir != "" {
		if err := ValidatePath(sourceDir); err != nil {
			return nil, err
		}
		roots = []string{sourceDir}
	}
	for _, root := range roots {
		values, err := readOptions(filepath.Join(root, "options.conf"))
		if err != nil {
			continue
		}
		if _, err = ReadFile(filepath.Join(root, "vhost.sh")); err != nil {
			continue
		}
		if _, err = ReadFile(filepath.Join(root, "install.sh")); err != nil {
			continue
		}
		r.SourceDir, r.options = root, values
		break
	}
	if sourceDir != "" && r.SourceDir == "" {
		return nil, errors.New("ONEINSTACK_SOURCE_INVALID: expected options.conf, install.sh and vhost.sh")
	}
	// Without the script directory, require its conventional Web/PHP layout.
	// Generic distribution packages must not be claimed by automatic adoption.
	if r.SourceDir == "" {
		if _, err := os.Stat("/usr/local/php/etc/php-fpm.conf"); err != nil {
			return r, nil
		}
		if _, err := os.Stat("/usr/local/nginx/conf/vhost"); err != nil {
			return r, nil
		}
		r.Warnings = append(r.Warnings, "SCRIPT_DIRECTORY_MISSING: detected the conventional OneinStack layout; database credentials cannot be imported")
	}
	for _, spec := range componentSpecs {
		dir := r.options[spec.option]
		if dir == "" {
			dir = spec.directory
		}
		c := Component{ID: spec.id, Key: spec.key, Name: spec.name, InstallDir: dir, WebRoot: r.options["wwwroot_dir"], LogRoot: r.options["wwwlogs_dir"], RuntimeGroup: spec.group, Scopes: spec.scopes}
		if c.WebRoot == "" {
			c.WebRoot = "/data/wwwroot"
		}
		if c.LogRoot == "" {
			c.LogRoot = "/data/wwwlogs"
		}
		if spec.web && (ValidatePath(c.WebRoot) != nil || ValidatePath(c.LogRoot) != nil) {
			r.Warnings = append(r.Warnings, spec.id+": website/log root cannot be safely adopted; original configuration is unchanged")
			continue
		}
		if err := ValidatePath(dir); err != nil {
			r.Warnings = append(r.Warnings, spec.id+": unsafe installation directory")
			continue
		}
		for _, relative := range spec.binaries {
			candidate := filepath.Join(dir, relative)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 && ValidatePath(candidate) == nil {
				c.Binary = candidate
				break
			}
		}
		if c.Binary == "" {
			continue
		}
		data, err := command(ctx, c.Binary, spec.versionArgs...)
		if err != nil {
			r.Warnings = append(r.Warnings, spec.id+": runtime version probe failed")
			continue
		}
		c.Version = runtimeVersion(spec.id, string(data))
		if c.Version == "" {
			r.Warnings = append(r.Warnings, spec.id+": runtime version is unavailable")
			continue
		}
		c.Config = filepath.Join(dir, spec.config)
		c.DataDir = r.options[strings.TrimSuffix(spec.option, "_install_dir")+"_data_dir"]
		if spec.group == "database" {
			if _, err := ReadFile(c.Config); err != nil {
				if _, err := ReadFile("/etc/my.cnf"); err == nil {
					c.Config = "/etc/my.cnf"
				}
			}
			c.Port = "3306"
			if data, err := ReadFile(c.Config); err == nil {
				section := ""
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "[") {
						section = strings.Trim(line, "[] ")
						continue
					}
					if section != "mysqld" && section != "server" {
						continue
					}
					key, value, ok := strings.Cut(line, "=")
					if !ok {
						continue
					}
					value, _ = staticValue(value)
					switch strings.TrimSpace(key) {
					case "port":
						if regexpPort(value) {
							c.Port = value
						}
					case "datadir":
						if ValidatePath(value) == nil {
							c.DataDir = value
						}
					}
				}
			}
		}
		for _, name := range spec.services {
			values, err := unit(ctx, name)
			if err != nil {
				continue
			}
			identityText := values["ExecStart"]
			if src := values["SourcePath"]; src != "" {
				if data, err := ReadFile(src); err == nil {
					identityText += "\n" + string(data)
				}
			}
			if !strings.Contains(identityText, c.Binary) && !strings.Contains(identityText, dir+"/") {
				continue
			}
			c.Service, c.UnitIdentity, c.ActiveState, c.CanReload = name, unitIdentity(values), values["ActiveState"], values["CanReload"] == "yes"
			break
		}
		if c.Service == "" {
			r.Warnings = append(r.Warnings, spec.id+": service identity is unverified; service controls are unavailable")
		}
		if spec.web {
			c.VhostDir = filepath.Join(filepath.Dir(c.Config), "vhost")
			r.scanSites(c)
		}
		r.Components = append(r.Components, c)
	}
	// The script supports additional PHP installations under php53..php85.
	for _, base := range append([]Component(nil), r.Components...) {
		if base.ID != "php" {
			continue
		}
		matches, _ := filepath.Glob(base.InstallDir + "[0-9][0-9]")
		for _, dir := range matches {
			c := base
			c.ID = "php-" + strings.TrimPrefix(dir, base.InstallDir)
			c.Key = c.ID
			c.InstallDir = dir
			c.Binary = filepath.Join(dir, "bin/php")
			if ValidatePath(c.Binary) != nil {
				continue
			}
			c.Config = filepath.Join(dir, "etc/php-fpm.conf")
			c.Service = ""
			c.UnitIdentity = ""
			data, err := command(ctx, c.Binary, "-v")
			if err != nil {
				continue
			}
			c.Version = runtimeVersion("php", string(data))
			if c.Version == "" {
				continue
			}
			name := "php" + strings.TrimPrefix(dir, base.InstallDir) + "-fpm"
			if v, err := unit(ctx, name); err == nil && strings.Contains(v["ExecStart"], dir+"/") {
				c.Service = name
				c.UnitIdentity = unitIdentity(v)
				c.ActiveState = v["ActiveState"]
				c.CanReload = v["CanReload"] == "yes"
			}
			c.Name = "PHP " + c.Version
			c.RuntimeGroup = "php-runtime-" + filepath.Base(dir)
			r.Components = append(r.Components, c)
		}
	}
	sort.Slice(r.Components, func(i, j int) bool { return r.Components[i].ID < r.Components[j].ID })
	sort.Slice(r.Sites, func(i, j int) bool {
		// LNMPA has matching front-end/backend vhost names. Bind the public
		// entry first while leaving the original Apache configuration in place.
		if (r.Sites[i].Engine == "apache") != (r.Sites[j].Engine == "apache") {
			return r.Sites[i].Engine != "apache"
		}
		return r.Sites[i].Config < r.Sites[j].Config
	})
	encoded, _ := json.Marshal(struct {
		SourceDir  string
		Components []Component
		Sites      []Site
	}{r.SourceDir, r.Components, r.Sites})
	r.Revision = digest(encoded)
	return r, ctx.Err()
}

type componentSpec struct {
	id, key, name, option, directory, config, group string
	binaries, services, versionArgs, scopes         []string
	web                                             bool
}

var componentSpecs = []componentSpec{
	{"nginx", "webserver", "Nginx", "nginx_install_dir", "/usr/local/nginx", "conf/nginx.conf", "web-server", []string{"sbin/nginx"}, []string{"nginx"}, []string{"-v"}, []string{"web_service"}, true},
	{"tengine", "tengine", "Tengine", "tengine_install_dir", "/usr/local/tengine", "conf/nginx.conf", "web-server", []string{"sbin/nginx", "sbin/tengine"}, []string{"nginx", "tengine"}, []string{"-v"}, []string{"web_service"}, true},
	{"openresty", "openresty", "OpenResty", "openresty_install_dir", "/usr/local/openresty", "nginx/conf/nginx.conf", "web-server", []string{"nginx/sbin/nginx"}, []string{"nginx", "openresty"}, []string{"-v"}, []string{"web_service"}, true},
	{"apache", "apache", "Apache", "apache_install_dir", "/usr/local/apache", "conf/httpd.conf", "web-server", []string{"bin/httpd"}, []string{"httpd", "apache2"}, []string{"-v"}, []string{"web_service"}, true},
	{"mysql", "db", "MySQL", "mysql_install_dir", "/usr/local/mysql", "my.cnf", "database", []string{"bin/mysqld"}, []string{"mysqld", "mysql"}, []string{"--version"}, []string{"database"}, false},
	{"mariadb", "mariadb", "MariaDB", "mariadb_install_dir", "/usr/local/mariadb", "my.cnf", "database", []string{"bin/mariadbd", "bin/mysqld"}, []string{"mysqld", "mariadb"}, []string{"--version"}, []string{"database"}, false},
	{"percona", "percona", "Percona", "percona_install_dir", "/usr/local/percona", "my.cnf", "database", []string{"bin/mysqld"}, []string{"mysqld", "mysql"}, []string{"--version"}, []string{"database"}, false},
	{"php", "php", "PHP-FPM", "php_install_dir", "/usr/local/php", "etc/php-fpm.conf", "php-runtime", []string{"bin/php"}, []string{"php-fpm"}, []string{"-v"}, []string{"runtime"}, false},
	{"redis", "redis", "Redis", "redis_install_dir", "/usr/local/redis", "etc/redis.conf", "", []string{"bin/redis-server"}, []string{"redis-server", "redis"}, []string{"--version"}, []string{"cache"}, false},
	{"memcached", "memcached", "Memcached", "memcached_install_dir", "/usr/local/memcached", "etc/memcached.conf", "", []string{"bin/memcached"}, []string{"memcached"}, []string{"-h"}, []string{"cache"}, false},
	{"postgresql", "postgresql", "PostgreSQL", "pgsql_install_dir", "/usr/local/pgsql", "data/postgresql.conf", "", []string{"bin/postgres"}, []string{"postgresql"}, []string{"--version"}, []string{"database"}, false},
	{"mongodb", "mongodb", "MongoDB", "mongo_install_dir", "/usr/local/mongodb", "mongodb.conf", "", []string{"bin/mongod"}, []string{"mongod"}, []string{"--version"}, []string{"database"}, false},
}

func (r *Report) scanSites(c Component) {
	files, err := filepath.Glob(filepath.Join(c.VhostDir, "*.conf"))
	if err != nil {
		return
	}
	if len(files) > 500 {
		r.Warnings = append(r.Warnings, c.ID+": more than 500 virtual hosts; automatic import skipped")
		return
	}
	for _, path := range files {
		data, err := ReadFile(path)
		if err != nil {
			r.Warnings = append(r.Warnings, c.ID+": virtual host cannot be read safely")
			continue
		}
		s := Site{Name: strings.TrimSuffix(filepath.Base(path), ".conf"), Engine: c.ID, Config: path, Type: "static", Fingerprint: digest(data)}
		if !safeName.MatchString(s.Name) {
			continue
		}
		if c.ID == "apache" {
			s.Domains = directive(string(data), "ServerName", false)
			if aliases := directive(string(data), "ServerAlias", false); aliases != "" {
				s.Domains += " " + aliases
			}
			s.Root = directive(string(data), "DocumentRoot", false)
			s.Certificate = directive(string(data), "SSLCertificateFile", false)
			s.AccessLog = firstField(directive(string(data), "CustomLog", false))
			s.ErrorLog = directive(string(data), "ErrorLog", false)
		} else {
			s.Domains = directive(string(data), "server_name", true)
			s.Root = directive(string(data), "root", true)
			s.AccessLog = firstField(directive(string(data), "access_log", true))
			s.ErrorLog = firstField(directive(string(data), "error_log", true))
			s.Certificate = directive(string(data), "ssl_certificate", true)
			s.PHPBackend = directive(string(data), "fastcgi_pass", true)
			s.Proxy = directive(string(data), "proxy_pass", true)
			if s.PHPBackend != "" || strings.Contains(string(data), "enable-php") {
				s.Type = "php"
			}
			if s.Proxy != "" {
				s.Type = "proxy"
			}
		}
		if s.Domains == "" || s.Domains == "_" {
			continue
		}
		if s.Root != "" {
			s.Root = strings.Trim(s.Root, "\"'")
			if ValidatePath(s.Root) != nil {
				r.Warnings = append(r.Warnings, s.Name+": custom root requires manual review")
				continue
			}
		}
		r.Sites = append(r.Sites, s)
	}
}

func firstField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "\"'")
}

func runtimeVersion(component, text string) string {
	if strings.HasPrefix(component, "php") {
		match := regexp.MustCompile(`(?m)^PHP\s+([0-9]+\.[0-9]+\.[0-9]+)`).FindStringSubmatch(text)
		if len(match) == 2 {
			return match[1]
		}
		return ""
	}
	return versionPattern.FindString(text)
}
func directive(text, name string, semicolon bool) string {
	ending := `[^\r\n#]+`
	if semicolon {
		ending = `[^;\r\n]+`
	}
	pattern := regexp.MustCompile(`(?mi)^\s*` + regexp.QuoteMeta(name) + `\s+(` + ending + `)`)
	match := pattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(match[1]), "\"'")
}

func DecodeComponent(metadata string) (Component, error) {
	var c Component
	err := json.Unmarshal([]byte(metadata), &c)
	if err != nil {
		return c, err
	}
	if !safeName.MatchString(c.ID) || ValidatePath(c.Binary) != nil || ValidatePath(c.InstallDir) != nil || (c.Config != "" && ValidatePath(c.Config) != nil) {
		return c, fmt.Errorf("invalid legacy component metadata")
	}
	return c, nil
}
func DecodeSite(metadata string) (Site, error) {
	var s Site
	err := json.Unmarshal([]byte(metadata), &s)
	if err != nil {
		return s, err
	}
	if !safeName.MatchString(s.Name) || ValidatePath(s.Config) != nil {
		return s, errors.New("invalid legacy site metadata")
	}
	return s, nil
}
