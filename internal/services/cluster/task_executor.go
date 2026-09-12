package cluster

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	softwareService "oneinstack/internal/services/software"
	websiteService "oneinstack/internal/services/website"
	"oneinstack/router/input"
)

const maxClusterFileBytes = 16 << 20

type softwareInstallTask struct {
	Key        string            `json:"key"`
	Version    string            `json:"version,omitempty"`
	Port       string            `json:"port,omitempty"`
	Username   string            `json:"username,omitempty"`
	Password   string            `json:"password,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

type softwareUninstallTask struct {
	Name                string            `json:"name"`
	Version             string            `json:"version,omitempty"`
	DataPolicy          string            `json:"dataPolicy,omitempty"`
	ConfirmDataDeletion bool              `json:"confirmDataDeletion,omitempty"`
	Parameters          map[string]string `json:"parameters,omitempty"`
}

type serviceTask struct{ Component, Action string }
type commandTask struct {
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}
type fileUploadTask struct {
	Path, ContentBase64, SHA256 string
	Mode                        uint32 `json:"mode,omitempty"`
}
type databaseSyncTask struct {
	Engine, Host, Port, Database, Username, Password string
	DumpBase64                                       string `json:"dumpBase64"`
}

func (a *Agent) executeExtendedTask(ctx context.Context, task *models.ClusterTask) (json.RawMessage, error) {
	switch task.Type {
	case "software.install":
		var p softwareInstallTask
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Key) == "" {
			return nil, errors.New("software key is required")
		}
		result, err := softwareService.RunInstall(&input.InstallParams{Key: p.Key, Version: p.Version, Port: p.Port, Username: p.Username, Pwd: p.Password, Parameters: p.Parameters})
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"key": p.Key, "installed": true, "result": result})
	case "software.uninstall":
		var p softwareUninstallTask
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return nil, err
		}
		if p.Name == "" {
			return nil, errors.New("software name is required")
		}
		removed, err := softwareService.Remove(&input.RemoveParams{Name: p.Name, Version: p.Version, DataPolicy: p.DataPolicy, ConfirmDataDeletion: p.ConfirmDataDeletion, Parameters: p.Parameters})
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"name": p.Name, "uninstalled": removed})
	case "service.start", "service.stop", "service.restart", "service.reload":
		var p serviceTask
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return nil, err
		}
		if p.Action == "" {
			p.Action = strings.TrimPrefix(task.Type, "service.")
		}
		return executeServiceTask(ctx, p)
	case "system.command":
		var p commandTask
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return nil, err
		}
		return executeCommandTask(ctx, p)
	case "file.upload":
		var p fileUploadTask
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return nil, err
		}
		return executeFileUpload(p)
	case "database.sync":
		var p databaseSyncTask
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return nil, err
		}
		return executeDatabaseSync(ctx, p)
	case "website.content_sync":
		var p WebsiteContentSyncPayload
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return nil, err
		}
		updated, err := websiteService.SyncClusterWebsite(ctx, p.Website, p.Settings)
		if err != nil {
			return nil, err
		}
		archiveData, err := base64.StdEncoding.DecodeString(p.ArchiveBase64)
		if err != nil || len(archiveData) > maxClusterFileBytes*4 {
			return nil, errors.New("website archive is invalid or exceeds 64 MiB")
		}
		if p.SHA256 != "" && !strings.EqualFold(fmt.Sprintf("%x", sha256.Sum256(archiveData)), p.SHA256) {
			return nil, errors.New("website archive SHA256 mismatch")
		}
		if err := extractWebsiteContent(updated.RootDir, archiveData); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"websiteId": updated.ID, "rootDir": updated.RootDir, "bytes": len(archiveData)})
	default:
		return nil, fmt.Errorf("unsupported cluster task type %q", task.Type)
	}
}

func extractWebsiteContent(root string, archiveData []byte) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) || !clusterManagedPath(root) {
		return errors.New("website root is invalid for content sync")
	}
	reader, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		return err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	var total int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return errors.New("website archive contains an unsafe path")
		}
		target := filepath.Join(root, name)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0750); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxWebsiteSyncBytes || total+header.Size > maxWebsiteSyncBytes {
			return errors.New("website archive contains an unsupported or oversized file")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".oneinstack-content-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		_, copyErr := io.CopyN(tmp, tarReader, header.Size)
		if copyErr == nil {
			copyErr = tmp.Chmod(os.FileMode(header.Mode & 0777))
		}
		if closeErr := tmp.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil {
			copyErr = os.Rename(tmpName, target)
		}
		if copyErr != nil {
			_ = os.Remove(tmpName)
			return copyErr
		}
		total += header.Size
	}
	return nil
}

func executeServiceTask(ctx context.Context, p serviceTask) (json.RawMessage, error) {
	if !softwareService.IsServiceAction(p.Action) {
		return nil, fmt.Errorf("unsupported service action %q", p.Action)
	}
	definition, err := softwareService.NormalizeServiceComponent(p.Component)
	if err != nil {
		return nil, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	unit := definition.ServiceName
	if err := exec.CommandContext(commandCtx, "systemctl", p.Action, unit).Run(); err != nil {
		return nil, fmt.Errorf("systemctl %s %s: %w", p.Action, unit, err)
	}
	return json.Marshal(map[string]string{"component": definition.Component, "action": p.Action, "service": unit})
}

func executeCommandTask(ctx context.Context, p commandTask) (json.RawMessage, error) {
	if len(p.Argv) == 0 || strings.TrimSpace(p.Argv[0]) == "" {
		return nil, errors.New("argv is required")
	}
	allowed := map[string]bool{"systemctl": true, "journalctl": true, "df": true, "du": true, "ip": true, "ss": true, "uname": true, "ps": true, "free": true, "ls": true, "find": true, "stat": true, "cat": true, "mkdir": true, "cp": true, "mv": true, "sha256sum": true, "id": true, "uptime": true}
	name := filepath.Base(p.Argv[0])
	if !allowed[name] {
		return nil, fmt.Errorf("command %q is not allowed", name)
	}
	for _, arg := range p.Argv {
		if len(arg) > 4096 {
			return nil, errors.New("command argument is too long")
		}
	}
	if p.TimeoutSeconds <= 0 || p.TimeoutSeconds > 600 {
		p.TimeoutSeconds = 120
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, p.Argv[0], p.Argv[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(output) > 64*1024 {
		output = output[:64*1024]
	}
	return json.Marshal(map[string]string{"command": strings.Join(p.Argv, " "), "output": string(output)})
}

func executeFileUpload(p fileUploadTask) (json.RawMessage, error) {
	path := filepath.Clean(strings.TrimSpace(p.Path))
	if path == "." || !filepath.IsAbs(path) || strings.Contains(path, ".."+string(filepath.Separator)) {
		return nil, errors.New("file path must be an absolute safe path")
	}
	data, err := base64.StdEncoding.DecodeString(p.ContentBase64)
	if err != nil || len(data) > maxClusterFileBytes {
		return nil, errors.New("file content is invalid or exceeds 16 MiB")
	}
	if p.SHA256 != "" {
		sum := fmt.Sprintf("%x", sha256.Sum256(data))
		if !strings.EqualFold(sum, p.SHA256) {
			return nil, errors.New("file SHA256 mismatch")
		}
	}
	if !clusterManagedPath(path) {
		return nil, errors.New("file path is outside managed directories")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".oneinstack-upload-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err == nil {
		mode := p.Mode & 0777
		if mode == 0 {
			mode = 0640
		}
		err = tmp.Chmod(os.FileMode(mode))
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"path": path, "bytes": len(data)})
}

func executeDatabaseSync(ctx context.Context, p databaseSyncTask) (json.RawMessage, error) {
	if p.Engine != "mysql" && p.Engine != "mariadb" && p.Engine != "postgres" && p.Engine != "postgresql" {
		return nil, errors.New("database engine must be mysql or postgres")
	}
	if p.Database == "" || p.Username == "" || p.DumpBase64 == "" {
		return nil, errors.New("database, username and dumpBase64 are required")
	}
	dump, err := base64.StdEncoding.DecodeString(p.DumpBase64)
	if err != nil || len(dump) > 64<<20 {
		return nil, errors.New("database dump is invalid or exceeds 64 MiB")
	}
	tmp, err := os.CreateTemp("", ".oneinstack-db-sync-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(dump); err == nil {
		err = tmp.Close()
	}
	if err != nil {
		return nil, err
	}
	host := p.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := p.Port
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if p.Engine == "mysql" || p.Engine == "mariadb" {
		args := []string{"--host", host, "--user", p.Username}
		if port != "" {
			args = append(args, "--port", port)
		}
		args = append(args, p.Database)
		cmd = exec.CommandContext(commandCtx, "mysql", args...)
		stdin, openErr := os.Open(name)
		if openErr != nil {
			return nil, openErr
		}
		defer stdin.Close()
		cmd.Stdin = stdin
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+p.Password)
	} else {
		args := []string{"--host", host, "--username", p.Username}
		if port != "" {
			args = append(args, "--port", port)
		}
		args = append(args, p.Database)
		cmd = exec.CommandContext(commandCtx, "psql", args...)
		stdin, openErr := os.Open(name)
		if openErr != nil {
			return nil, openErr
		}
		defer stdin.Close()
		cmd.Stdin = stdin
		cmd.Env = append(os.Environ(), "PGPASSWORD="+p.Password)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("database restore failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return json.Marshal(map[string]any{"engine": p.Engine, "database": p.Database, "bytes": len(dump)})
}

// clusterManagedPath limits node file operations to directories owned by the
// panel. This prevents a task token from writing arbitrary system files.
func clusterManagedPath(path string) bool {
	path = filepath.Clean(path)
	roots := []string{
		app.GetBasePath(),
		app.ONE_CONFIG.System.WebPath,
		app.ONE_CONFIG.System.LogPath,
		app.ONE_CONFIG.System.WebVhostRoot,
	}
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || root == string(filepath.Separator) {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
