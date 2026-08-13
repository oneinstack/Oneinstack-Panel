package panelbackup

import (
	"errors"
	"time"
)

const (
	BackupSchemaVersion = 1
	EncryptionSchema    = 1

	StatusIdle          = "idle"
	StatusValidating    = "validating"
	StatusStopping      = "stopping"
	StatusRestoring     = "restoring"
	StatusHealthCheck   = "health_checking"
	StatusSucceeded     = "succeeded"
	StatusFailed        = "failed"
	StatusRolledBack    = "rolled_back"
	StatusRollbackError = "rollback_failed"
)

var (
	ErrNotFound          = errors.New("panel backup not found")
	ErrInvalidBackup     = errors.New("invalid panel backup")
	ErrInvalidPassphrase = errors.New("invalid panel backup passphrase")
	ErrRestoreBusy       = errors.New("panel restore is already running")
	ErrRecoveryNeeded    = errors.New("an interrupted panel restore must be recovered")
)

// ValidationStage identifies the safe, high-level stage at which a backup
// failed validation. It is intentionally free of paths, SQL, and secrets.
type ValidationStage string

const (
	ValidationStageIntegrity ValidationStage = "integrity"
	ValidationStageDecrypt   ValidationStage = "decrypt"
	ValidationStageManifest  ValidationStage = "manifest"
	ValidationStageConfig    ValidationStage = "config"
	ValidationStageDatabase  ValidationStage = "database"
)

type validationStageError struct {
	stage ValidationStage
	err   error
}

func (e *validationStageError) Error() string { return string(e.stage) + ": " + e.err.Error() }
func (e *validationStageError) Unwrap() error { return e.err }

func withValidationStage(stage ValidationStage, err error) error {
	if err == nil {
		return nil
	}
	return &validationStageError{stage: stage, err: err}
}

// ValidationStageOf returns a coarse validation stage for safe diagnostics.
func ValidationStageOf(err error) ValidationStage {
	var stageErr *validationStageError
	if errors.As(err, &stageErr) {
		return stageErr.stage
	}
	return "unknown"
}

// ValidationFailureMessage is safe to persist in restore status and logs.
// It deliberately omits the wrapped error because it may contain paths or
// implementation details from the host.
func ValidationFailureMessage(err error) string {
	switch ValidationStageOf(err) {
	case ValidationStageIntegrity:
		return "备份文件完整性校验失败"
	case ValidationStageDecrypt:
		return "备份解密校验失败"
	case ValidationStageManifest:
		return "备份清单校验失败"
	case ValidationStageConfig:
		return "备份配置校验失败"
	case ValidationStageDatabase:
		return "备份数据库校验失败"
	default:
		return "备份恢复预检失败"
	}
}

type Config struct {
	BasePath        string
	ConfigPath      string
	DatabasePath    string
	CertificatePath string
	BackupRoot      string
	MaxBackupBytes  int64
	MaxFiles        int
}

type CreateOptions struct {
	Passphrase          string
	IncludeCertificates bool
}

type BackupInfo struct {
	ID                   string    `json:"id"`
	FileName             string    `json:"fileName"`
	CreatedAt            time.Time `json:"createdAt"`
	PanelVersion         string    `json:"panelVersion"`
	Size                 int64     `json:"size"`
	SHA256               string    `json:"sha256"`
	FileCount            int       `json:"fileCount"`
	IncludesCertificates bool      `json:"includesCertificates"`
	Imported             bool      `json:"imported"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion        int            `json:"schemaVersion"`
	CreatedAt            time.Time      `json:"createdAt"`
	PanelVersion         string         `json:"panelVersion"`
	IncludesCertificates bool           `json:"includesCertificates"`
	Files                []ManifestFile `json:"files"`
}

type PreflightResult struct {
	Backup               BackupInfo `json:"backup"`
	SchemaVersion        int        `json:"schemaVersion"`
	PanelVersion         string     `json:"panelVersion"`
	FileCount            int        `json:"fileCount"`
	IncludesCertificates bool       `json:"includesCertificates"`
	ConfigValid          bool       `json:"configValid"`
	DatabaseValid        bool       `json:"databaseValid"`
}

type RestoreRequest struct {
	BackupID   string `json:"backupId"`
	Passphrase string `json:"passphrase"`
}

type RestoreStatus struct {
	State             string     `json:"state"`
	BackupID          string     `json:"backupId,omitempty"`
	Message           string     `json:"message,omitempty"`
	RollbackAttempted bool       `json:"rollbackAttempted"`
	RollbackSucceeded bool       `json:"rollbackSucceeded"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
}
