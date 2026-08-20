package approval

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type ExpiryManager struct {
	db       *gorm.DB
	interval time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewExpiryManager(db *gorm.DB, interval time.Duration) (*ExpiryManager, error) {
	if db == nil {
		return nil, errors.New("approval expiry manager database is not configured")
	}
	if interval <= 0 {
		return nil, errors.New("approval expiry interval must be positive")
	}
	return &ExpiryManager{db: db, interval: interval}, nil
}

func (manager *ExpiryManager) Start() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.cancel = cancel
	manager.done = make(chan struct{})
	go manager.run(ctx, manager.done)
}

func (manager *ExpiryManager) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	manager.runOnce()
	ticker := time.NewTicker(manager.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			manager.runOnce()
		}
	}
}

func (manager *ExpiryManager) runOnce() {
	count, err := ExpirePending(manager.db, time.Now().UTC())
	if err != nil {
		log.Printf("approval expiry reconciliation failed: %v", err)
		return
	}
	if count > 0 {
		log.Printf("approval expiry reconciliation marked %d requests as expired", count)
	}
}

func ExpirePending(db *gorm.DB, now time.Time) (int64, error) {
	if db == nil {
		return 0, errors.New("approval database is not initialized")
	}
	result := db.Model(&models.ApprovalRequest{}).
		Where("status = ? AND expires_at <= ?", models.ApprovalStatusPending, now.UTC()).
		Update("status", models.ApprovalStatusExpired)
	return result.RowsAffected, result.Error
}

func (manager *ExpiryManager) Stop(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	cancel := manager.cancel
	done := manager.done
	manager.cancel = nil
	manager.done = nil
	manager.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
