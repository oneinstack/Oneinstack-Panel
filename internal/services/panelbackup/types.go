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
