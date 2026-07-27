package script

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/router/input"
)

func TestValidateParametersAppliesDefaultsAndRejectsUnsafeValues(t *testing.T) {
	info := &ScriptInfo{
		Params: map[string]string{"PORT": "6379"},
		ParameterSpecs: []ParameterSpec{
			{Name: "PORT", Type: "port", Required: true},
			{Name: "INSTALL_DIR", Type: "path", Default: "/usr/local/redis"},
		},
	}
	if err := validateParameters(info); err != nil {
		t.Fatalf("validateParameters: %v", err)
	}
	if info.Params["INSTALL_DIR"] != "/usr/local/redis" {
		t.Fatalf("default was not applied: %#v", info.Params)
	}

	info.Params["PORT"] = "70000"
	if err := validateParameters(info); err == nil {
		t.Fatal("expected invalid port to be rejected")
	}
	info.Params["PORT"] = "6379"
	info.ParameterSpecs = append(info.ParameterSpecs, ParameterSpec{Name: "PATH", Type: "string"})
	if err := validateParameters(info); err == nil {
		t.Fatal("expected reserved environment variable to be rejected")
	}
}

func TestAcquireComponentExecutionIsNonBlocking(t *testing.T) {
	release, acquired := acquireComponentExecution("test-component-lock")
	if !acquired {
		t.Fatal("expected first operation to acquire lock")
	}
	if _, second := acquireComponentExecution("test-component-lock"); second {
		t.Fatal("expected concurrent operation to be rejected")
	}
	release()
	releaseAgain, acquiredAgain := acquireComponentExecution("test-component-lock")
	if !acquiredAgain {
		t.Fatal("expected lock to be reusable")
	}
	releaseAgain()
}

func TestRunActionEnforcesTimeout(t *testing.T) {
	directory := t.TempDir()
	action := filepath.Join(directory, "timeout.sh")
	if err := os.WriteFile(action, []byte("#!/usr/bin/env bash\nsleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.CreateTemp(t.TempDir(), "action-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	manager := NewScriptManager()
	err = manager.runAction(action, directory, nil, 20*time.Millisecond, logFile)
	if err == nil || !strings.Contains(err.Error(), "exceeded timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestUpdateSoftwareInstallInfoMovesInstalledVersion(t *testing.T) {
	if err := app.InitDB(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", "webserver", "1.24.0").
		Updates(map[string]any{"installed": true, "install_version": "1.24.0"}).Error; err != nil {
		t.Fatal(err)
	}

	manager := NewScriptManager()
	manager.updateSoftwareInstallInfo(&input.InstallParams{
		Key: "webserver", Version: "1.28.2",
	}, true, "1.28.2")

	var oldVersion, newVersion models.Software
	if err := app.DB().Where("`key` = ? AND version = ?", "webserver", "1.24.0").First(&oldVersion).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Where("`key` = ? AND version = ?", "webserver", "1.28.2").First(&newVersion).Error; err != nil {
		t.Fatal(err)
	}
	if oldVersion.Installed || oldVersion.InstallVersion != "" {
		t.Fatalf("old version remained installed: %#v", oldVersion)
	}
	if !newVersion.Installed || newVersion.InstallVersion != "1.28.2" {
		t.Fatalf("new version was not installed: %#v", newVersion)
	}
}

func TestRedactingWriterRedactsSecretsAcrossWriteBoundaries(t *testing.T) {
	var output bytes.Buffer
	writer := newRedactingWriter(&output, []string{"repeat-a-secret", "xy"})
	for _, chunk := range []string{"prefix repeat-a", "-sec", "ret suffix x", "y done"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	value := output.String()
	if strings.Contains(value, "repeat-a-secret") || strings.Contains(value, "xy") {
		t.Fatalf("secret leaked from redacting writer: %q", value)
	}
	if value != "prefix [REDACTED] suffix [REDACTED] done" {
		t.Fatalf("redacted output = %q", value)
	}
}

func TestRunActionReadsStructuredProgressFromFD3(t *testing.T) {
	directory := t.TempDir()
	action := filepath.Join(directory, "progress.sh")
	if err := os.WriteFile(action, []byte(
		"#!/usr/bin/env bash\n"+
			"printf '%s\\n' '{\"type\":\"progress\",\"percent\":42,\"code\":\"compile\",\"message\":\"正在编译\"}' >&3\n"+
			"echo regular-output\n",
	), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	observer := &recordingExecutionObserver{}
	manager := NewScriptManager()
	if err := manager.runActionContext(
		context.Background(),
		"install",
		action,
		directory,
		nil,
		time.Second,
		&output,
		observer,
	); err != nil {
		t.Fatal(err)
	}
	if output.String() != "regular-output\n" {
		t.Fatalf("ordinary output = %q", output.String())
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.progress) != 1 ||
		observer.progress[0].percent != 42 ||
		observer.progress[0].code != "compile" ||
		observer.progress[0].message != "正在编译" {
		t.Fatalf("unexpected progress events: %#v", observer.progress)
	}
}

func TestExecuteScriptTaskUninstallUpdatesInstalledStateOnlyOnSuccess(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		action        string
		wantInstalled bool
		wantStatus    int
		wantError     bool
	}{
		{
			name:          "successful uninstall",
			action:        "#!/usr/bin/env bash\nset -euo pipefail\necho removed\n",
			wantInstalled: false,
			wantStatus:    models.Soft_Status_Default,
		},
		{
			name:          "failed uninstall",
			action:        "#!/usr/bin/env bash\nset -euo pipefail\necho failed >&2\nexit 9\n",
			wantInstalled: true,
			wantStatus:    models.Soft_Status_Err,
			wantError:     true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := app.InitDB(filepath.Join(t.TempDir(), "panel.db")); err != nil {
				t.Fatal(err)
			}
			if err := app.DB().Model(&models.Software{}).
				Where("`key` = ? AND version = ?", "redis", "7.4.8").
				Updates(map[string]any{
					"installed":       true,
					"install_version": "7.4.8",
					"status":          models.Soft_Status_Suc,
				}).Error; err != nil {
				t.Fatal(err)
			}

			directory := t.TempDir()
			action := filepath.Join(directory, "uninstall.sh")
			if err := os.WriteFile(action, []byte(testCase.action), 0700); err != nil {
				t.Fatal(err)
			}
			manager := NewScriptManager()
			manager.tempDir = directory
			manager.logDir = directory
			_, err := manager.ExecuteScriptTask(
				context.Background(),
				&ScriptInfo{
					Name:       "redis",
					Type:       ScriptTypeUninstall,
					Path:       action,
					WorkingDir: directory,
					Params:     map[string]string{"SOFTWARE_VERSION": "7.4.8"},
					ActionName: "uninstall",
					Timeouts:   map[string]time.Duration{"uninstall": time.Second},
				},
				&input.InstallParams{Key: "redis", Version: "7.4.8"},
				filepath.Join(directory, "task.log"),
				&recordingExecutionObserver{},
			)
			if (err != nil) != testCase.wantError {
				t.Fatalf("ExecuteScriptTask error = %v, wantError %v", err, testCase.wantError)
			}

			var installed models.Software
			if err := app.DB().
				Where("`key` = ? AND version = ?", "redis", "7.4.8").
				First(&installed).Error; err != nil {
				t.Fatal(err)
			}
			if installed.Installed != testCase.wantInstalled ||
				installed.Status != testCase.wantStatus {
				t.Fatalf("unexpected software state after uninstall: %#v", installed)
			}
			if testCase.wantInstalled && installed.InstallVersion != "7.4.8" {
				t.Fatalf("failed uninstall cleared installed version: %#v", installed)
			}
			if !testCase.wantInstalled && installed.InstallVersion != "" {
				t.Fatalf("successful uninstall retained installed version: %#v", installed)
			}
		})
	}
}

func TestExecuteScriptTaskServiceActionPreservesInstallationState(t *testing.T) {
	if err := app.InitDB(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", "redis", "7.4.8").
		Updates(map[string]any{
			"installed":       true,
			"install_version": "7.4.8",
			"status":          models.Soft_Status_Suc,
			"log":             "original.log",
		}).Error; err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	action := filepath.Join(directory, "restart.sh")
	if err := os.WriteFile(action, []byte(
		"#!/usr/bin/env bash\nset -euo pipefail\necho restarted\n",
	), 0700); err != nil {
		t.Fatal(err)
	}
	manager := NewScriptManager()
	manager.tempDir = directory
	manager.logDir = directory
	if _, err := manager.ExecuteScriptTask(
		context.Background(),
		&ScriptInfo{
			Name:       "redis",
			Type:       ScriptType("restart"),
			Path:       action,
			WorkingDir: directory,
			Params:     map[string]string{"SOFTWARE_VERSION": "7.4.8"},
			ActionName: "restart",
			Timeouts:   map[string]time.Duration{"restart": time.Second},
		},
		&input.InstallParams{Key: "redis", Version: "7.4.8"},
		filepath.Join(directory, "task.log"),
		&recordingExecutionObserver{},
	); err != nil {
		t.Fatal(err)
	}
	var installed models.Software
	if err := app.DB().
		Where("`key` = ? AND version = ?", "redis", "7.4.8").
		First(&installed).Error; err != nil {
		t.Fatal(err)
	}
	if !installed.Installed ||
		installed.InstallVersion != "7.4.8" ||
		installed.Status != models.Soft_Status_Suc ||
		installed.Log != "original.log" {
		t.Fatalf("service action changed installation state: %#v", installed)
	}
}

func TestExecuteProbeRejectsOversizedStatusOutput(t *testing.T) {
	directory := t.TempDir()
	action := filepath.Join(directory, "status.sh")
	if err := os.WriteFile(action, []byte(
		"#!/usr/bin/env bash\nset -euo pipefail\nprintf '1234567890'\n",
	), 0700); err != nil {
		t.Fatal(err)
	}
	manager := NewScriptManager()
	_, err := manager.ExecuteProbe(context.Background(), &ScriptInfo{
		Name:       "redis",
		Path:       action,
		WorkingDir: directory,
		Params:     map[string]string{},
		ActionName: "status",
		Timeouts:   map[string]time.Duration{"status": time.Second},
	}, 5)
	if err == nil || !strings.Contains(err.Error(), "exceeded limit") {
		t.Fatalf("expected bounded probe error, got %v", err)
	}
}

func TestExecuteScriptTaskConfigurationPreservesInstallationState(t *testing.T) {
	if err := app.InitDB(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", "redis", "7.4.8").
		Updates(map[string]any{
			"installed":       true,
			"install_version": "7.4.8",
			"status":          models.Soft_Status_Suc,
			"log":             "install.log",
		}).Error; err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	action := filepath.Join(directory, "config.sh")
	if err := os.WriteFile(action, []byte(
		"#!/usr/bin/env bash\nset -euo pipefail\necho configured\n",
	), 0700); err != nil {
		t.Fatal(err)
	}
	manager := NewScriptManager()
	manager.tempDir = directory
	manager.logDir = directory
	if _, err := manager.ExecuteScriptTask(
		context.Background(),
		&ScriptInfo{
			Name:       "redis",
			Type:       ScriptType("configApply"),
			Path:       action,
			WorkingDir: directory,
			Params:     map[string]string{"SOFTWARE_VERSION": "7.4.8"},
			ActionName: "configApply",
			Timeouts:   map[string]time.Duration{"configApply": time.Second},
		},
		&input.InstallParams{Key: "redis", Version: "7.4.8"},
		filepath.Join(directory, "task.log"),
		&recordingExecutionObserver{},
	); err != nil {
		t.Fatal(err)
	}
	var installed models.Software
	if err := app.DB().
		Where("`key` = ? AND version = ?", "redis", "7.4.8").
		First(&installed).Error; err != nil {
		t.Fatal(err)
	}
	if !installed.Installed ||
		installed.InstallVersion != "7.4.8" ||
		installed.Status != models.Soft_Status_Suc ||
		installed.Log != "install.log" {
		t.Fatalf("configuration action changed installation state: %#v", installed)
	}
}

func TestExecuteProbeAllowsConfigurationReadAction(t *testing.T) {
	directory := t.TempDir()
	action := filepath.Join(directory, "config.sh")
	if err := os.WriteFile(action, []byte(
		"#!/usr/bin/env bash\nset -euo pipefail\nprintf 'component=redis\\n'\n",
	), 0700); err != nil {
		t.Fatal(err)
	}
	manager := NewScriptManager()
	output, err := manager.ExecuteProbe(context.Background(), &ScriptInfo{
		Name:       "redis",
		Path:       action,
		WorkingDir: directory,
		Params:     map[string]string{},
		ActionName: "configGet",
		Timeouts:   map[string]time.Duration{"configGet": time.Second},
	}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "component=redis\n" {
		t.Fatalf("unexpected configuration probe output: %q", output)
	}
}

type recordedProgress struct {
	percent int
	code    string
	message string
}

type recordingExecutionObserver struct {
	mu       sync.Mutex
	progress []recordedProgress
}

func (o *recordingExecutionObserver) OnActionStart(string) {}

func (o *recordingExecutionObserver) OnActionProgress(_ string, percent int, code, message string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.progress = append(o.progress, recordedProgress{
		percent: percent,
		code:    code,
		message: message,
	})
}

func (o *recordingExecutionObserver) OnActionComplete(string) {}

func (o *recordingExecutionObserver) OnRollbackStart() {}

func (o *recordingExecutionObserver) OnRollbackComplete(error) {}
