package certificate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"oneinstack/internal/models"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// RenewalScheduler keeps certificate status current and queues renewals before
// expiry. A failed renewal is delayed by Manager for 24 hours to avoid a hot
// retry loop against the CA.
type RenewalScheduler struct {
	manager  *Manager
	db       *gorm.DB
	schedule string
	cron     *cron.Cron
	start    sync.Once
	stop     sync.Once
}

func NewRenewalScheduler(manager *Manager, schedule string) (*RenewalScheduler, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("certificate manager is required")
	}
	parser := cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
	)
	if _, err := parser.Parse(schedule); err != nil {
		return nil, fmt.Errorf("invalid ACME renewal schedule: %w", err)
	}
	return &RenewalScheduler{
		manager:  manager,
		db:       manager.db,
		schedule: schedule,
		cron:     cron.New(cron.WithParser(parser)),
	}, nil
}

func (scheduler *RenewalScheduler) Start() error {
	var startErr error
	scheduler.start.Do(func() {
		if err := scheduler.manager.Start(); err != nil {
			startErr = err
			return
		}
		if _, err := scheduler.cron.AddFunc(scheduler.schedule, func() {
			_ = scheduler.Scan(context.Background())
		}); err != nil {
			startErr = err
			return
		}
		scheduler.cron.Start()
		go func() {
			_ = scheduler.Scan(context.Background())
		}()
	})
	return startErr
}

func (scheduler *RenewalScheduler) Scan(ctx context.Context) error {
	now := time.Now().UTC()
	var certificates []models.Certificate
	if err := scheduler.db.Where("status <> ?", models.CertificateStatusDisabled).
		Find(&certificates).Error; err != nil {
		return err
	}
	var scanErr error
	for _, certificate := range certificates {
		select {
		case <-ctx.Done():
			return errors.Join(scanErr, ctx.Err())
		default:
		}
		status := certificateStatus(certificate.NotAfter, now, certificate.RenewBeforeDays)
		if err := scheduler.db.Model(&models.Certificate{}).
			Where("id = ?", certificate.ID).
			Update("status", status).Error; err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		if !certificate.AutoRenew {
			continue
		}
		due := certificate.NotAfter.Before(
			now.Add(time.Duration(certificate.RenewBeforeDays) * 24 * time.Hour),
		)
		if certificate.NextRenewAt != nil && certificate.NextRenewAt.After(now) {
			due = false
		}
		if !due {
			continue
		}
		if _, err := scheduler.manager.SubmitRenew(certificate.ID, 0); err != nil {
			var active int64
			activeErr := scheduler.db.Model(&models.CertificateTask{}).
				Where("website_id = ? AND status IN ?", certificate.WebsiteID, models.ActiveCertificateTaskStatuses()).
				Count(&active).Error
			if activeErr != nil || active == 0 {
				scanErr = errors.Join(scanErr, err)
			}
		}
	}
	return scanErr
}

func (scheduler *RenewalScheduler) Stop(ctx context.Context) error {
	var stopContext context.Context
	scheduler.stop.Do(func() {
		stopContext = scheduler.cron.Stop()
	})
	if stopContext == nil {
		return nil
	}
	select {
	case <-stopContext.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
