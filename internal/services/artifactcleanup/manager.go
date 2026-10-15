package artifactcleanup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/models"
	"oneinstack/internal/services/audit"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"golang.org/x/sys/unix"
	"gorm.io/gorm"
)

var (
	ErrCandidateChanged = errors.New("ARTIFACT_CANDIDATE_CHANGED")
	ErrUnsafeCandidate  = errors.New("ARTIFACT_UNSAFE_CANDIDATE")
	ErrTaskActive       = errors.New("ARTIFACT_TASK_ACTIVE")
	defaultMu           sync.Mutex
	defaultManager      *Manager
)

type Manager struct {
	db        *gorm.DB
	cron      *cron.Cron
	startOnce sync.Once
	startErr  error
	runMu     sync.Mutex
}

func Default(db *gorm.DB) (*Manager, error) {
	if db == nil {
		return nil, errors.New("artifact cleanup database unavailable")
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultManager == nil || defaultManager.db != db {
		defaultManager = &Manager{db: db}
	}
	return defaultManager, defaultManager.Start()
}

func (m *Manager) Start() error {
	m.startOnce.Do(func() {
		if err := m.db.AutoMigrate(&models.ArtifactCleanupTask{}); err != nil {
			m.startErr = err
			return
		}
		if err := m.db.Model(&models.ArtifactCleanupTask{}).
			Where("status IN ?", []string{"queued", "running"}).
			Updates(map[string]any{"status": "interrupted", "error_code": "PANEL_RESTARTED", "error_text": "Panel restarted before cleanup finished"}).Error; err != nil {
			m.startErr = err
			return
		}
		m.cron = cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger), cron.Recover(cron.DefaultLogger)))
		if _, err := m.cron.AddFunc("45 3 * * *", func() {
			if err := m.RunScheduled(); err != nil {
				log.Printf("artifact cleanup schedule failed: %v", err)
			}
		}); err != nil {
			m.startErr = err
			return
		}
		m.cron.Start()
	})
	return m.startErr
}

func (m *Manager) Stop(ctx context.Context) error {
	if m == nil || m.cron == nil {
		return nil
	}
	stopped := m.cron.Stop()
	select {
	case <-stopped.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Busy() (bool, error) {
	var count int64
	err := m.db.Model(&models.SoftwareTask{}).Where("status IN ?", models.ActiveSoftwareTaskStatuses()).Count(&count).Error
	return count > 0, err
}

func (m *Manager) Find(id string) (Candidate, error) {
	if len(id) != 64 {
		return Candidate{}, ErrCandidateChanged
	}
	inventory, err := List(time.Now())
	if err != nil {
		return Candidate{}, err
	}
	for _, candidate := range inventory.Candidates {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return Candidate{}, ErrCandidateChanged
}

func (m *Manager) Submit(id string, userID int64, automatic bool) (*models.ArtifactCleanupTask, error) {
	return m.submit(id, userID, automatic, true)
}

// RunCLI uses the same candidate validation, task persistence, lock and audit path.
func (m *Manager) RunCLI(id string) (*models.ArtifactCleanupTask, error) {
	task, err := m.submit(id, -1, false, false)
	if err != nil {
		return nil, err
	}
	m.run(task.ID)
	if err := m.db.First(task, "id = ?", task.ID).Error; err != nil {
		return nil, err
	}
	return task, nil
}

func (m *Manager) submit(id string, userID int64, automatic, asynchronous bool) (*models.ArtifactCleanupTask, error) {
	if userID == 0 && !automatic {
		return nil, errors.New("manual cleanup requires an operator")
	}
	candidate, err := m.Find(id)
	if err != nil {
		return nil, err
	}
	if !candidate.Eligible || (automatic && !candidate.Automatic) {
		return nil, ErrUnsafeCandidate
	}
	busy, err := m.Busy()
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, ErrTaskActive
	}
	var active int64
	if err := m.db.Model(&models.ArtifactCleanupTask{}).
		Where("candidate_id = ? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, ErrTaskActive
	}
	task := &models.ArtifactCleanupTask{
		ID: uuid.NewString(), CandidateID: id, Kind: candidate.Kind, Component: candidate.Component,
		Path: candidate.Path, Status: "queued", Bytes: candidate.Bytes, RequestedBy: userID,
		Automatic: automatic, CreatedAt: time.Now(),
	}
	if err := m.db.Create(task).Error; err != nil {
		return nil, err
	}
	if asynchronous {
		go m.run(task.ID)
	}
	return task, nil
}

func (m *Manager) ListTasks(limit int) ([]models.ArtifactCleanupTask, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	var tasks []models.ArtifactCleanupTask
	err := m.db.Order("created_at DESC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (m *Manager) RunScheduled() error {
	space, err := DiskSpace()
	if err != nil {
		return err
	}
	if space.Warning {
		log.Printf("artifact cleanup warning: root filesystem available=%d bytes ratio=%.1f%%", space.AvailableBytes, space.AvailableRatio*100)
	}
	busy, err := m.Busy()
	if err != nil || busy {
		return err
	}
	inventory, err := List(time.Now())
	if err != nil {
		return err
	}
	for _, candidate := range inventory.Candidates {
		if !candidate.Eligible || !candidate.Automatic {
			continue
		}
		if _, err := m.Submit(candidate.ID, 0, true); err != nil && !errors.Is(err, ErrTaskActive) && !errors.Is(err, ErrCandidateChanged) {
			log.Printf("artifact cleanup skipped %s: %v", candidate.ID, err)
		}
	}
	return nil
}

func (m *Manager) run(id string) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	var task models.ArtifactCleanupTask
	if err := m.db.First(&task, "id = ?", id).Error; err != nil {
		log.Printf("artifact cleanup task lookup failed: %v", err)
		return
	}
	started := time.Now()
	if err := m.db.Model(&task).Updates(map[string]any{"status": "running", "started_at": started}).Error; err != nil {
		log.Printf("artifact cleanup task start failed: %v", err)
		return
	}
	err := m.removeCandidate(task)
	finished := time.Now()
	status, code, message := "succeeded", "", ""
	if err != nil {
		status, code, message = "failed", errorCode(err), err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
	}
	if updateErr := m.db.Model(&task).Updates(map[string]any{
		"status": status, "error_code": code, "error_text": message, "finished_at": finished,
	}).Error; updateErr != nil {
		log.Printf("artifact cleanup task completion persistence failed: %v", updateErr)
	}
	if manager := audit.Default(); manager != nil {
		_, auditErr := manager.Append(audit.EventInput{EventType: "operation", Action: "artifact.cleanup", Method: "TASK",
			Route: "artifact.cleanup", Path: "/artifacts", Outcome: status, UserID: task.RequestedBy,
			Message: fmt.Sprintf("task=%s candidate=%s kind=%s bytes=%d code=%s", task.ID, task.CandidateID, task.Kind, task.Bytes, code)})
		if auditErr != nil {
			log.Printf("artifact cleanup audit failed: %v", auditErr)
		}
	}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrCandidateChanged):
		return "ARTIFACT_CANDIDATE_CHANGED"
	case errors.Is(err, ErrUnsafeCandidate):
		return "ARTIFACT_UNSAFE_CANDIDATE"
	case errors.Is(err, ErrTaskActive):
		return "ARTIFACT_TASK_ACTIVE"
	default:
		return "ARTIFACT_CLEANUP_FAILED"
	}
}

func (m *Manager) removeCandidate(task models.ArtifactCleanupTask) error {
	// The same advisory lock is used by new component download scripts.
	lockFD, err := unix.Open("/run/lock/oneinstack-artifacts.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Close(lockFD)
	if err := unix.Flock(lockFD, unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(lockFD, unix.LOCK_UN)
	busy, err := m.Busy()
	if err != nil {
		return err
	}
	if busy {
		return ErrTaskActive
	}
	candidate, err := m.Find(task.CandidateID)
	if err != nil {
		return err
	}
	if candidate.Path != task.Path || candidate.Kind != task.Kind || !candidate.Eligible || (task.Automatic && !candidate.Automatic) {
		return ErrCandidateChanged
	}
	// Never accept a path from the request or task without rediscovering it in the fixed roots.
	if !strings.HasPrefix(candidate.Path, cacheRoot+"/") && !strings.HasPrefix(candidate.Path, componentStateRoot()+"/") {
		return ErrUnsafeCandidate
	}
	parentFD, err := openDirectoryNoFollow(filepath.Dir(candidate.Path))
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	name := filepath.Base(candidate.Path)
	var parentStat, stat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil {
		return err
	}
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Dev != parentStat.Dev || uint64(stat.Dev) != candidate.device ||
		stat.Ino != candidate.inode || stat.Size != candidate.fileSize ||
		stat.Mtim.Sec*1e9+stat.Mtim.Nsec != candidate.modNano {
		return ErrCandidateChanged
	}
	if candidate.Kind == "uninstall_archive" {
		return removeDirectoryAt(parentFD, name, &stat)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrUnsafeCandidate
	}
	for _, sidecar := range []string{name + ".verified", name + ".asc"} {
		var sidecarStat unix.Stat_t
		if err := unix.Fstatat(parentFD, sidecar, &sidecarStat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			if sidecarStat.Mode&unix.S_IFMT != unix.S_IFREG || sidecarStat.Dev != parentStat.Dev {
				return ErrUnsafeCandidate
			}
			if err := unix.Unlinkat(parentFD, sidecar, 0); err != nil {
				return err
			}
		} else if !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return unix.Unlinkat(parentFD, name, 0)
}

func openDirectoryNoFollow(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, ErrUnsafeCandidate
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func removeDirectoryAt(parentFD int, name string, expected *unix.Stat_t) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var root unix.Stat_t
	if err := unix.Fstat(fd, &root); err != nil {
		return err
	}
	if expected == nil || root.Dev != expected.Dev || root.Ino != expected.Ino || root.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrCandidateChanged
	}
	names, err := file.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, child := range names {
		if child == "." || child == ".." || strings.ContainsRune(child, '/') {
			return ErrUnsafeCandidate
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(fd, child, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Dev != root.Dev {
			return ErrUnsafeCandidate
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := removeDirectoryAt(fd, child, &stat); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := unix.Unlinkat(fd, child, 0); err != nil {
				return err
			}
		default:
			return ErrUnsafeCandidate
		}
	}
	return unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
}
