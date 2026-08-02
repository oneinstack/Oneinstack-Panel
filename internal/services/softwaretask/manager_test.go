package softwaretask

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestManagerPersistsProgressWithoutSecretParameters(t *testing.T) {
	db := openTaskTestDB(t)
	logDir := t.TempDir()
	manager := NewManager(db, logDir, func(
		ctx context.Context,
		request InstallRequest,
		logPath string,
		reporter *Reporter,
	) error {
		if request.Password != "task-secret-value" {
			t.Errorf("executor password = %q", request.Password)
		}
		if err := os.WriteFile(logPath, []byte("install output\n"), 0600); err != nil {
			return err
		}
		reporter.OnPackageResolved("2.0.2", "remote")
		reporter.OnActionStart("precheck")
		reporter.OnActionComplete("precheck")
		reporter.OnActionStart("install")
		reporter.OnActionProgress("install", 50, "compile", "正在编译")
		reporter.OnActionComplete("install")
		reporter.OnActionStart("verify")
		reporter.OnActionComplete("verify")
		return nil
	})
	task, err := manager.Submit(InstallRequest{
		Key:      "webserver",
		Version:  "1.28.2",
		Port:     "80",
		Username: "operator",
		Password: "task-secret-value",
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	task = waitForTaskStatus(t, manager, task.ID, models.SoftwareTaskStatusSucceeded)
	if task.Progress != 100 || task.EventSeq < 5 {
		t.Fatalf("unexpected completed task: %#v", task)
	}
	if task.ResolvedVersion != "2.0.2" || task.PackageSource != "remote" {
		t.Fatalf("package resolution was not persisted: %#v", task)
	}
	if strings.Contains(task.ParametersJSON, "task-secret-value") {
		t.Fatalf("task parameters persisted secret: %s", task.ParametersJSON)
	}
	events, err := manager.EventsAfter(task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 5 || events[len(events)-1].Type != "terminal" {
		t.Fatalf("unexpected task events: %#v", events)
	}
	chunk, err := manager.ReadLog(task.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Content != "install output\n" || !chunk.EOF {
		t.Fatalf("unexpected log chunk: %#v", chunk)
	}
}

func TestManagerCancelsRunningTask(t *testing.T) {
	db := openTaskTestDB(t)
	started := make(chan struct{})
	manager := NewManager(db, t.TempDir(), func(
		ctx context.Context,
		request InstallRequest,
		logPath string,
		reporter *Reporter,
	) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	task, err := manager.Submit(InstallRequest{
		Key: "redis", Version: "7.4.8",
	}, 9)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	if _, err := manager.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	task = waitForTaskStatus(t, manager, task.ID, models.SoftwareTaskStatusCanceled)
	if !task.CancelRequested || task.ErrorCode != "ACTION_CANCELED" {
		t.Fatalf("unexpected canceled task: %#v", task)
	}
}

func TestManagerRejectsMutuallyExclusiveDatabaseInstall(t *testing.T) {
	db := openTaskTestDB(t)
	if err := db.Create(&models.Software{
		Name:      "MySQL",
		Key:       "db",
		Component: "mysql",
		Version:   "8.0",
		Installed: true,
		Status:    models.Soft_Status_Suc,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Software{
		Name:        "MariaDB",
		Key:         "mariadb",
		Component:   "mariadb",
		Version:     "10.11",
		Installable: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, t.TempDir(), func(
		context.Context,
		InstallRequest,
		string,
		*Reporter,
	) error {
		t.Fatal("conflicting install reached executor")
		return nil
	})
	_, err := manager.Submit(InstallRequest{
		Key: "mariadb", Version: "10.11",
	}, 9)
	if err == nil || !strings.Contains(err.Error(), "MySQL") {
		t.Fatalf("expected MySQL conflict, got %v", err)
	}
}

func TestManagerRunsUninstallAsDurableTask(t *testing.T) {
	db := openTaskTestDB(t)
	if err := db.Create(&models.Software{
		Name:           "Redis",
		Key:            "redis",
		Version:        "7.4.8",
		Installed:      true,
		InstallVersion: "7.4.8",
		Status:         models.Soft_Status_Suc,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, t.TempDir(), func(
		ctx context.Context,
		request InstallRequest,
		logPath string,
		reporter *Reporter,
	) error {
		if request.Operation != "uninstall" ||
			request.Key != "redis" ||
			request.Version != "7.4.8" {
			t.Errorf("unexpected uninstall request: %#v", request)
		}
		if err := os.WriteFile(logPath, []byte("uninstall output\n"), 0600); err != nil {
			return err
		}
		reporter.OnActionStart("uninstall")
		reporter.OnActionProgress("uninstall", 50, "remove_binary", "正在移除程序文件")
		reporter.OnActionComplete("uninstall")
		return nil
	})
	task, err := manager.SubmitUninstall("redis", "", 11)
	if err != nil {
		t.Fatal(err)
	}
	task = waitForTaskStatus(t, manager, task.ID, models.SoftwareTaskStatusSucceeded)
	if task.Operation != "uninstall" ||
		task.RequestedVersion != "7.4.8" ||
		!strings.Contains(task.Message, "数据") {
		t.Fatalf("unexpected completed uninstall task: %#v", task)
	}
	events, err := manager.EventsAfter(task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundUninstall := false
	for _, event := range events {
		if event.Status == models.SoftwareTaskStatusUninstalling &&
			event.Phase == "uninstall" {
			foundUninstall = true
			break
		}
	}
	if !foundUninstall {
		t.Fatalf("uninstall progress was not persisted: %#v", events)
	}
}

func TestManagerRunsServiceActionAsDurableTask(t *testing.T) {
	db := openTaskTestDB(t)
	if err := db.Create(&models.Software{
		Name:           "Nginx",
		Key:            "webserver",
		Version:        "1.28.2",
		Installed:      true,
		InstallVersion: "1.28.2",
		Status:         models.Soft_Status_Suc,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, t.TempDir(), func(
		ctx context.Context,
		request InstallRequest,
		logPath string,
		reporter *Reporter,
	) error {
		if request.Operation != "restart" ||
			request.Key != "webserver" ||
			request.Version != "1.28.2" {
			t.Errorf("unexpected service request: %#v", request)
		}
		if err := os.WriteFile(logPath, []byte("service restart output\n"), 0600); err != nil {
			return err
		}
		reporter.OnActionStart("restart")
		reporter.OnActionProgress("restart", 50, "service_restarting", "正在重启 Nginx")
		reporter.OnActionComplete("restart")
		return nil
	})
	task, err := manager.SubmitServiceAction("nginx", "restart", 12)
	if err != nil {
		t.Fatal(err)
	}
	task = waitForTaskStatus(t, manager, task.ID, models.SoftwareTaskStatusSucceeded)
	if task.Operation != "restart" ||
		task.RequestedVersion != "1.28.2" ||
		task.Message != "服务重启成功" {
		t.Fatalf("unexpected completed service task: %#v", task)
	}
	events, err := manager.EventsAfter(task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundRestarting := false
	for _, event := range events {
		if event.Status == models.SoftwareTaskStatusRestarting &&
			event.Phase == "restart" {
			foundRestarting = true
			break
		}
	}
	if !foundRestarting {
		t.Fatalf("service progress was not persisted: %#v", events)
	}
}

func TestManagerRejectsServiceActionForUninstalledComponent(t *testing.T) {
	manager := NewManager(openTaskTestDB(t), t.TempDir(), func(
		context.Context,
		InstallRequest,
		string,
		*Reporter,
	) error {
		return nil
	})
	if _, err := manager.SubmitServiceAction("redis", "start", 13); err == nil ||
		!strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected not installed error, got %v", err)
	}
}

func TestManagerRunsConfigurationAsDurableTask(t *testing.T) {
	db := openTaskTestDB(t)
	if err := db.Create(&models.Software{
		Name:           "Redis",
		Key:            "redis",
		Version:        "7.4.8",
		Installed:      true,
		InstallVersion: "7.4.8",
		Status:         models.Soft_Status_Suc,
	}).Error; err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 64)
	manager := NewManager(db, t.TempDir(), func(
		ctx context.Context,
		request InstallRequest,
		logPath string,
		reporter *Reporter,
	) error {
		if request.Operation != "configure" ||
			request.Version != "7.4.8" ||
			request.Revision != revision ||
			request.Configuration["maxmemory"] != "512" {
			t.Errorf("unexpected configuration request: %#v", request)
		}
		if err := os.WriteFile(logPath, []byte("configuration output\n"), 0600); err != nil {
			return err
		}
		reporter.OnActionStart("configApply")
		reporter.OnActionProgress("configApply", 62, "config_publish", "正在发布配置")
		reporter.OnActionComplete("configApply")
		return nil
	})
	previous := map[string]string{
		"maxmemory":       "0",
		"maxmemoryPolicy": "noeviction",
		"appendonly":      "false",
		"timeout":         "0",
		"tcpKeepalive":    "300",
	}
	target := map[string]string{
		"maxmemory":       "512",
		"maxmemoryPolicy": "allkeys-lru",
		"appendonly":      "true",
		"timeout":         "0",
		"tcpKeepalive":    "300",
	}
	task, err := manager.SubmitConfiguration(
		"redis",
		revision,
		previous,
		target,
		"",
		14,
	)
	if err != nil {
		t.Fatal(err)
	}
	task = waitForTaskStatus(t, manager, task.ID, models.SoftwareTaskStatusSucceeded)
	if task.Operation != "configure" ||
		task.Message != "配置已安全发布并验证成功" ||
		!strings.Contains(task.ParametersJSON, `"maxmemory":"512"`) {
		t.Fatalf("unexpected configuration task: %#v", task)
	}
	events, err := manager.EventsAfter(task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundConfiguring := false
	for _, event := range events {
		if event.Status == models.SoftwareTaskStatusConfiguring &&
			event.Phase == "config_apply" {
			foundConfiguring = true
			break
		}
	}
	if !foundConfiguring {
		t.Fatalf("configuration progress was not persisted: %#v", events)
	}
	var history models.SoftwareConfigurationHistory
	if err := db.First(&history, "task_id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if history.Status != models.SoftwareConfigurationStatusSucceeded ||
		history.SoftwareVersion != "7.4.8" ||
		!strings.Contains(history.BeforeJSON, `"maxmemory":"0"`) ||
		!strings.Contains(history.AfterJSON, `"maxmemory":"512"`) {
		t.Fatalf("unexpected configuration history: %#v", history)
	}
}

func TestManagerMarksFailedConfigurationHistory(t *testing.T) {
	db := openTaskTestDB(t)
	if err := db.Create(&models.Software{
		Name:           "Redis",
		Key:            "redis",
		Version:        "7.4.8",
		Installed:      true,
		InstallVersion: "7.4.8",
		Status:         models.Soft_Status_Suc,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, t.TempDir(), func(
		context.Context,
		InstallRequest,
		string,
		*Reporter,
	) error {
		return errors.New("configApply action failed: exit status 65")
	})
	values := map[string]string{
		"maxmemory":       "0",
		"maxmemoryPolicy": "noeviction",
		"appendonly":      "false",
		"timeout":         "0",
		"tcpKeepalive":    "300",
	}
	target := cloneConfigurationValues(values)
	target["maxmemory"] = "512"
	task, err := manager.SubmitConfiguration(
		"redis",
		strings.Repeat("b", 64),
		values,
		target,
		"",
		14,
	)
	if err != nil {
		t.Fatal(err)
	}
	task = waitForTaskStatus(t, manager, task.ID, models.SoftwareTaskStatusFailed)
	var history models.SoftwareConfigurationHistory
	if err := db.First(&history, "task_id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if history.Status != models.SoftwareConfigurationStatusFailed ||
		history.FinishedAt == nil {
		t.Fatalf("unexpected failed configuration history: %#v", history)
	}
}

func TestClassifyConfigurationExecutionErrors(t *testing.T) {
	tests := map[string]string{
		"configApply action failed: exit status 75": "CONFIG_CONFLICT",
		"configApply action failed: exit status 65": "CONFIG_VALIDATE_FAILED",
		"configApply action failed: exit status 1":  "CONFIG_APPLY_FAILED",
		"configApply action exceeded timeout 3m0s":  "ACTION_TIMEOUT",
	}
	for message, expected := range tests {
		if actual := classifyExecutionError(errors.New(message)); actual != expected {
			t.Fatalf("classifyExecutionError(%q) = %q, want %q", message, actual, expected)
		}
	}
}

func TestClassifyPackageResolutionErrors(t *testing.T) {
	tests := map[string]string{
		"resolve openresty uninstall package: no compatible package":                "PACKAGE_UNAVAILABLE",
		"resolve nginx installer: script center connectivity check failed":          "CENTER_UNAVAILABLE",
		"resolve redis status package: script center is not ready: HTTP 503":        "CENTER_UNAVAILABLE",
		"resolve php installer: download package: HTTP 502":                         "CENTER_UNAVAILABLE",
		"resolve mysql uninstall package: package does not provide required action": "PACKAGE_UNAVAILABLE",
	}
	for message, expected := range tests {
		if actual := classifyExecutionError(errors.New(message)); actual != expected {
			t.Fatalf("classifyExecutionError(%q) = %q, want %q", message, actual, expected)
		}
	}
}

func TestManagerStopSafelyInterruptsRunningTask(t *testing.T) {
	db := openTaskTestDB(t)
	started := make(chan struct{})
	manager := NewManager(db, t.TempDir(), func(
		ctx context.Context,
		request InstallRequest,
		logPath string,
		reporter *Reporter,
	) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	task, err := manager.Submit(InstallRequest{
		Key: "redis", Version: "7.4.8",
	}, 9)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
	task, err = manager.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != models.SoftwareTaskStatusInterrupted ||
		task.ErrorCode != "PANEL_SHUTDOWN" {
		t.Fatalf("unexpected shutdown task: %#v", task)
	}
	if _, err := manager.Submit(InstallRequest{
		Key: "redis", Version: "7.4.8",
	}, 9); err == nil {
		t.Fatal("stopping manager accepted a new task")
	}
}

func TestManagerMarksPreviousActiveTasksInterrupted(t *testing.T) {
	db := openTaskTestDB(t)
	now := time.Now().Add(-time.Minute)
	task := &models.SoftwareTask{
		ID:               "previous-task",
		Operation:        "install",
		Component:        "nginx",
		SoftwareKey:      "webserver",
		RequestedVersion: "1.28.2",
		Status:           models.SoftwareTaskStatusInstalling,
		Phase:            models.SoftwareTaskStatusInstalling,
		RollbackStatus:   models.SoftwareTaskRollbackNotRequired,
		RequestedBy:      1,
		EventSeq:         1,
		LogPath:          filepath.Join(t.TempDir(), "previous.log"),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ComponentOperationLock{
		Component: "nginx", TaskID: task.ID, AcquiredAt: now, HeartbeatAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	manager := NewManager(db, t.TempDir(), func(
		ctx context.Context,
		request InstallRequest,
		logPath string,
		reporter *Reporter,
	) error {
		return nil
	})
	manager.SetRecoveryInspector(func(context.Context, *models.SoftwareTask) RecoveryInspection {
		return RecoveryInspection{
			Status:  "running_unrecorded",
			Message: "检测到组件正在运行，但安装记录尚未提交",
		}
	})
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	task, err := manager.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != models.SoftwareTaskStatusInterrupted ||
		task.ErrorCode != "PANEL_RESTARTED" ||
		task.RecoveryStatus != "running_unrecorded" ||
		!strings.Contains(task.RecoveryMessage, "正在运行") {
		t.Fatalf("task was not reconciled: %#v", task)
	}
	var locks int64
	if err := db.Model(&models.ComponentOperationLock{}).Count(&locks).Error; err != nil {
		t.Fatal(err)
	}
	if locks != 0 {
		t.Fatalf("stale component locks = %d", locks)
	}
}

func openTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "task.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Software{},
		&models.SoftwareTask{},
		&models.SoftwareTaskEvent{},
		&models.ComponentOperationLock{},
		&models.SoftwareConfigurationHistory{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func waitForTaskStatus(t *testing.T, manager *Manager, taskID, status string) *models.SoftwareTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.Get(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == status {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, err := manager.Get(taskID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task status = %s, want %s", task.Status, status)
	return nil
}
