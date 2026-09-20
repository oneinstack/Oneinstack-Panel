package componentstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	OwnershipFileName      = "panel-ownership.json"
	OwnershipSchemaVersion = 1
	maximumOwnershipSize   = 256 * 1024
)

// Ownership is the component-neutral contract between a successful install
// and destructive Panel removal. Component packages declare purge-owned path
// parameters in their signed manifest; Panel records the resolved values here
// so a later Panel installation can remove the component without knowing its
// name or implementation details.
type Ownership struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Component       string            `json:"component"`
	SoftwareVersion string            `json:"softwareVersion"`
	PackageVersion  string            `json:"packageVersion,omitempty"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	PurgePaths      []string          `json:"purgePaths,omitempty"`
}

func NormalizeParameterName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	previousWasLowerOrDigit := false
	lastWasSeparator := false
	for _, character := range value {
		if unicode.IsUpper(character) {
			if previousWasLowerOrDigit && !lastWasSeparator {
				builder.WriteByte('-')
			}
			builder.WriteRune(unicode.ToLower(character))
			previousWasLowerOrDigit = false
			lastWasSeparator = false
			continue
		}
		if unicode.IsLower(character) || unicode.IsDigit(character) {
			builder.WriteRune(unicode.ToLower(character))
			previousWasLowerOrDigit = true
			lastWasSeparator = false
			continue
		}
		switch character {
		case '-', '_', '.', ' ':
			if builder.Len() > 0 && !lastWasSeparator {
				builder.WriteByte('-')
				lastWasSeparator = true
			}
		default:
			return ""
		}
		previousWasLowerOrDigit = false
	}
	return strings.Trim(builder.String(), "-")
}

func IsSensitiveParameter(name string) bool {
	compact := strings.ReplaceAll(NormalizeParameterName(name), "-", "")
	return compact == "pwd" || compact == "key" || compact == "credential" ||
		strings.Contains(compact, "password") || strings.Contains(compact, "passwd") ||
		strings.Contains(compact, "secret") || strings.Contains(compact, "token") ||
		strings.Contains(compact, "credential") || strings.Contains(compact, "privatekey")
}

func IsStateRootParameter(name string) bool {
	compact := strings.ReplaceAll(NormalizeParameterName(name), "-", "")
	return compact == "componentstatedir" || compact == "oneinstackcomponentstate"
}

// IsLegacyPurgeParameter provides a component-neutral bridge for signed
// packages created before the explicit purge flag existed. It recognizes only
// conventional owned directory roles, never arbitrary path parameters such as
// sockets, certificates, uploads, or offline bundles.
func IsLegacyPurgeParameter(name string) bool {
	normalized := NormalizeParameterName(name)
	for _, suffix := range []string{"install-dir", "data-dir", "data-root", "log-dir", "web-root", "web-vhost-root"} {
		if normalized == suffix || strings.HasSuffix(normalized, "-"+suffix) {
			return true
		}
	}
	return false
}

func Read(stateDir string) (*Ownership, error) {
	path := filepath.Join(stateDir, OwnershipFileName)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximumOwnershipSize {
		return nil, errors.New("ownership state must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var state Ownership
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("ownership state contains multiple JSON values")
		}
		return nil, err
	}
	if err := Validate(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func Write(stateDir string, state *Ownership) error {
	if err := Validate(state); err != nil {
		return err
	}
	cleanedStateDir := filepath.Clean(strings.TrimSpace(stateDir))
	if !filepath.IsAbs(cleanedStateDir) || cleanedStateDir == "/" {
		return errors.New("component state directory must be a scoped absolute path")
	}
	if err := os.MkdirAll(cleanedStateDir, 0750); err != nil {
		return err
	}
	if info, err := os.Lstat(cleanedStateDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err != nil {
			return err
		}
		return errors.New("component state directory must be a real directory")
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(cleanedStateDir, ".panel-ownership.*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0640); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(cleanedStateDir, OwnershipFileName))
}

func Remove(stateDir string) error {
	cleanedStateDir := filepath.Clean(strings.TrimSpace(stateDir))
	if !filepath.IsAbs(cleanedStateDir) || cleanedStateDir == "/" {
		return errors.New("component state directory must be a scoped absolute path")
	}
	path := filepath.Join(cleanedStateDir, OwnershipFileName)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("ownership state must be a regular file")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	_ = os.Remove(cleanedStateDir)
	return nil
}

func Validate(state *Ownership) error {
	if state == nil {
		return errors.New("ownership state is required")
	}
	if state.SchemaVersion != OwnershipSchemaVersion {
		return fmt.Errorf("unsupported ownership schema version %d", state.SchemaVersion)
	}
	if !validComponentName(state.Component) {
		return errors.New("ownership component is invalid")
	}
	if strings.TrimSpace(state.SoftwareVersion) == "" || strings.ContainsAny(state.SoftwareVersion, "\x00\r\n") {
		return errors.New("ownership software version is invalid")
	}
	normalizedParameters := make(map[string]string, len(state.Parameters))
	for key, value := range state.Parameters {
		normalized := NormalizeParameterName(key)
		value = strings.TrimSpace(value)
		if normalized == "" || IsSensitiveParameter(normalized) || value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("ownership parameter %q is invalid", key)
		}
		if existing := normalizedParameters[normalized]; existing != "" && existing != value {
			return fmt.Errorf("ownership parameter %q is duplicated", normalized)
		}
		normalizedParameters[normalized] = value
	}
	state.Parameters = normalizedParameters
	paths := make([]string, 0, len(state.PurgePaths))
	seen := make(map[string]bool, len(state.PurgePaths))
	for _, value := range state.PurgePaths {
		cleaned, err := validatePurgePath(value)
		if err != nil {
			return err
		}
		if !seen[cleaned] {
			paths = append(paths, cleaned)
			seen[cleaned] = true
		}
	}
	sort.Strings(paths)
	state.PurgePaths = paths
	state.Component = strings.ToLower(strings.TrimSpace(state.Component))
	state.SoftwareVersion = strings.TrimSpace(state.SoftwareVersion)
	state.PackageVersion = strings.TrimSpace(state.PackageVersion)
	return nil
}

func validatePurgePath(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("ownership purge path contains control characters")
	}
	cleaned := filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(cleaned) {
		return "", errors.New("ownership purge path must be absolute")
	}
	switch cleaned {
	case "/", "/usr", "/usr/local", "/etc", "/var", "/var/lib", "/data", "/home", "/root", "/tmp":
		return "", errors.New("ownership purge path is too broad")
	}
	return cleaned, nil
}

func validComponentName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 2 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
		if index == 0 && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}
