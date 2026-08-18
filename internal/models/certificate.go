package models

import "time"

const (
	CertificateStatusActive   = "active"
	CertificateStatusExpiring = "expiring"
	CertificateStatusExpired  = "expired"
	CertificateStatusDisabled = "disabled"
	CertificateStatusError    = "error"
)

const (
	CertificateTaskStatusQueued      = "queued"
	CertificateTaskStatusRunning     = "running"
	CertificateTaskStatusCanceling   = "canceling"
	CertificateTaskStatusSucceeded   = "succeeded"
	CertificateTaskStatusFailed      = "failed"
	CertificateTaskStatusCanceled    = "canceled"
	CertificateTaskStatusInterrupted = "interrupted"
)

const (
	CertificateTaskOperationIssue      = "issue"
	CertificateTaskOperationRenew      = "renew"
	CertificateTaskOperationUpload     = "upload"
	CertificateTaskOperationSelfSigned = "self_signed"
	CertificateTaskOperationBind       = "bind"
)

// Certificate stores deployment metadata. Private key and certificate paths
// are never serialized to API clients.
type Certificate struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	WebsiteID       int64      `json:"websiteId" gorm:"not null;uniqueIndex"`
	ManagedID       string     `json:"managedId,omitempty" gorm:"size:36;index"`
	Provider        string     `json:"provider" gorm:"size:32;not null"`
	Email           string     `json:"email" gorm:"size:254;not null"`
	Domains         string     `json:"domains" gorm:"type:text;not null"`
	DirectoryURL    string     `json:"-" gorm:"size:1024;not null"`
	CertificatePath string     `json:"-" gorm:"size:1024;not null"`
	PrivateKeyPath  string     `json:"-" gorm:"size:1024;not null"`
	SerialNumber    string     `json:"serialNumber" gorm:"size:128"`
	Issuer          string     `json:"issuer" gorm:"size:512"`
	Status          string     `json:"status" gorm:"size:32;not null;index"`
	AutoRenew       bool       `json:"autoRenew" gorm:"not null;default:true"`
	RenewBeforeDays int        `json:"renewBeforeDays" gorm:"not null;default:30"`
	ForceHTTPS      bool       `json:"forceHttps" gorm:"column:force_https;not null;default:false"`
	NotBefore       time.Time  `json:"notBefore"`
	NotAfter        time.Time  `json:"notAfter" gorm:"index"`
	LastRenewAt     *time.Time `json:"lastRenewAt,omitempty"`
	NextRenewAt     *time.Time `json:"nextRenewAt,omitempty" gorm:"index"`
	LastError       string     `json:"lastError,omitempty" gorm:"size:1024"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// ManagedCertificate is the canonical certificate resource. WebsiteID on the
// legacy Certificate model remains as a deployment compatibility projection.
type ManagedCertificate struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	Provider        string    `json:"provider" gorm:"size:32;not null"`
	Domains         string    `json:"domains" gorm:"type:text;not null"`
	CertificatePath string    `json:"-" gorm:"size:1024;not null"`
	PrivateKeyPath  string    `json:"-" gorm:"size:1024;not null"`
	SerialNumber    string    `json:"serialNumber" gorm:"size:128"`
	Issuer          string    `json:"issuer" gorm:"size:512"`
	Algorithm       string    `json:"algorithm" gorm:"size:32;not null"`
	Status          string    `json:"status" gorm:"size:32;not null;index"`
	AutoRenew       bool      `json:"autoRenew" gorm:"not null;default:false"`
	RenewBeforeDays int       `json:"renewBeforeDays" gorm:"not null;default:30"`
	NotBefore       time.Time `json:"notBefore"`
	NotAfter        time.Time `json:"notAfter" gorm:"index"`
	Remark          string    `json:"remark" gorm:"size:512"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func (ManagedCertificate) TableName() string { return "managed_certificate" }

type CertificateBinding struct {
	ID                   string     `json:"id" gorm:"primaryKey;size:36"`
	ManagedCertificateID string     `json:"certificateId" gorm:"size:36;not null;uniqueIndex:idx_certificate_binding_target"`
	WebsiteID            int64      `json:"websiteId" gorm:"not null;uniqueIndex:idx_certificate_binding_target"`
	Status               string     `json:"status" gorm:"size:32;not null;index"`
	ForceHTTPS           bool       `json:"forceHttps" gorm:"not null;default:false"`
	LastError            string     `json:"lastError,omitempty" gorm:"size:1024"`
	DeployedAt           *time.Time `json:"deployedAt,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

func (CertificateBinding) TableName() string { return "certificate_binding" }

type DNSAccount struct {
	ID                   string    `json:"id" gorm:"primaryKey;size:36"`
	Name                 string    `json:"name" gorm:"size:128;not null"`
	Provider             string    `json:"provider" gorm:"size:32;not null;index"`
	CredentialOne        string    `json:"-" gorm:"type:text;not null"`
	CredentialTwo        string    `json:"-" gorm:"type:text"`
	CredentialConfigured bool      `json:"credentialConfigured" gorm:"not null;default:false"`
	Enabled              bool      `json:"enabled" gorm:"not null;default:true"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

func (DNSAccount) TableName() string { return "certificate_dns_account" }

func (Certificate) TableName() string {
	return "certificate"
}

// CertificateTask is a restart-safe task snapshot. The bounded LogText field
// is intentionally hidden and exposed only through the dedicated log API.
type CertificateTask struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	Operation       string     `json:"operation" gorm:"size:16;not null"`
	WebsiteID       int64      `json:"websiteId" gorm:"not null;index:idx_certificate_task_site_created"`
	WebsiteName     string     `json:"websiteName" gorm:"size:253;not null"`
	CertificateID   string     `json:"certificateId,omitempty" gorm:"size:36;index"`
	ManagedID       string     `json:"managedId,omitempty" gorm:"size:36;index"`
	Email           string     `json:"email" gorm:"size:254;not null"`
	Domains         string     `json:"domains" gorm:"type:text;not null"`
	DirectoryURL    string     `json:"-" gorm:"size:1024;not null"`
	AutoRenew       bool       `json:"autoRenew" gorm:"not null;default:true"`
	RenewBeforeDays int        `json:"renewBeforeDays" gorm:"not null;default:30"`
	ForceHTTPS      bool       `json:"forceHttps" gorm:"column:force_https;not null;default:false"`
	Algorithm       string     `json:"algorithm,omitempty" gorm:"size:32"`
	ValidityYears   int        `json:"validityYears,omitempty" gorm:"default:10"`
	Remark          string     `json:"remark,omitempty" gorm:"size:512"`
	InputCertPath   string     `json:"-" gorm:"size:1024"`
	InputKeyPath    string     `json:"-" gorm:"size:1024"`
	Status          string     `json:"status" gorm:"size:32;not null;index:idx_certificate_task_status_created"`
	Progress        int        `json:"progress" gorm:"not null;default:0"`
	Message         string     `json:"message" gorm:"size:512"`
	ErrorCode       string     `json:"errorCode,omitempty" gorm:"size:64"`
	ErrorMessage    string     `json:"errorMessage,omitempty" gorm:"size:1024"`
	RequestedBy     int64      `json:"requestedBy" gorm:"not null"`
	CancelRequested bool       `json:"cancelRequested" gorm:"not null;default:false"`
	LogText         string     `json:"-" gorm:"type:text"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"index:idx_certificate_task_site_created,priority:2;index:idx_certificate_task_status_created,priority:2"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (CertificateTask) TableName() string {
	return "certificate_task"
}

type CertificateOperationLock struct {
	WebsiteID  int64     `json:"-" gorm:"primaryKey"`
	TaskID     string    `json:"-" gorm:"size:36;not null;uniqueIndex"`
	AcquiredAt time.Time `json:"-"`
}

func (CertificateOperationLock) TableName() string {
	return "certificate_operation_lock"
}

func IsCertificateTaskTerminal(status string) bool {
	switch status {
	case CertificateTaskStatusSucceeded,
		CertificateTaskStatusFailed,
		CertificateTaskStatusCanceled,
		CertificateTaskStatusInterrupted:
		return true
	default:
		return false
	}
}

func ActiveCertificateTaskStatuses() []string {
	return []string{
		CertificateTaskStatusQueued,
		CertificateTaskStatusRunning,
		CertificateTaskStatusCanceling,
	}
}
