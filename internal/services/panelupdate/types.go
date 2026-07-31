package panelupdate

import (
	"context"
	"errors"
	"time"
)

const (
	ManifestSchemaVersion = 1
	MaxManifestBytes      = 1 << 20

	StateIdle           = "idle"
	StateChecking       = "checking"
	StateDownloading    = "downloading"
	StatePreflight      = "preflight"
	StateSwitching      = "switching"
	StateHealthChecking = "health_checking"
	StateSucceeded      = "succeeded"
	StateFailed         = "failed"
	StateRolledBack     = "rolled_back"
	StateRollbackFailed = "rollback_failed"
	StateRecoveryNeeded = "recovery_required"
)

var (
	ErrDisabled        = errors.New("panel updates are disabled")
	ErrInvalidManifest = errors.New("invalid update manifest")
	ErrNoUpdate        = errors.New("no newer update is available")
	ErrUpdateBusy      = errors.New("another panel update is running")
	ErrRecoveryNeeded  = errors.New("an interrupted panel update must be rolled back before retrying")
)

type Config struct {
	Enabled          bool
	ManifestURL      string
	ResolveURL       string
	KeyStatusURL     string
	TrustStatePath   string
	InstanceID       string
	Channel          string
	TrustedKeys      map[string]string
	RequestTimeout   time.Duration
	MaxPackageBytes  int64
	MaxExpandedBytes int64
	HealthTimeout    time.Duration
	BackupRetention  int
	InstallDir       string
	HealthURL        string
	CurrentVersion   string
	OS               string
	Arch             string
}

type Manifest struct {
	SchemaVersion  int               `json:"schemaVersion"`
	Version        string            `json:"version"`
	Channel        string            `json:"channel"`
	PublishedAt    time.Time         `json:"publishedAt"`
	MinimumVersion string            `json:"minimumVersion,omitempty"`
	ReleaseNotes   string            `json:"releaseNotes,omitempty"`
	Artifacts      []Artifact        `json:"artifacts"`
	Signature      ManifestSignature `json:"signature"`
}

type Artifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	FileName string `json:"fileName"`
}

type ManifestSignature struct {
	KeyID string `json:"keyId"`
	Value string `json:"value"`
}

type CheckResult struct {
	Enabled         bool      `json:"enabled"`
	Source          string    `json:"source,omitempty"`
	InstanceID      string    `json:"instanceId,omitempty"`
	CurrentVersion  string    `json:"currentVersion"`
	LatestVersion   string    `json:"latestVersion,omitempty"`
	UpdateAvailable bool      `json:"updateAvailable"`
	Channel         string    `json:"channel"`
	PublishedAt     time.Time `json:"publishedAt,omitempty"`
	ReleaseNotes    string    `json:"releaseNotes,omitempty"`
	MinimumVersion  string    `json:"minimumVersion,omitempty"`
	Compatible      bool      `json:"compatible"`
	ArtifactSize    int64     `json:"artifactSize,omitempty"`
	SigningKeyID    string    `json:"signingKeyId,omitempty"`
	TrustRevision   uint64    `json:"trustRevision,omitempty"`
	TrustSource     string    `json:"trustSource,omitempty"`
	TrustedKeyCount int       `json:"trustedKeyCount"`
	RevokedKeyCount int       `json:"revokedKeyCount"`
	TrustUpdatedAt  time.Time `json:"trustUpdatedAt,omitempty"`
}

type Status struct {
	State             string     `json:"state"`
	CurrentVersion    string     `json:"currentVersion,omitempty"`
	TargetVersion     string     `json:"targetVersion,omitempty"`
	Message           string     `json:"message,omitempty"`
	BackupPath        string     `json:"backupPath,omitempty"`
	RollbackAttempted bool       `json:"rollbackAttempted"`
	RollbackSucceeded bool       `json:"rollbackSucceeded"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
}

type Command struct {
	Name string
	Args []string
	Env  map[string]string
}

type CommandRunner interface {
	Run(ctx context.Context, command Command) ([]byte, error)
}

type ServiceController interface {
	IsActive(ctx context.Context) bool
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
}

type HealthChecker interface {
	WaitReady(ctx context.Context, url string, timeout time.Duration) error
}
