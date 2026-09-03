package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"oneinstack/app"
	"oneinstack/internal/services/panelbackup"
	"oneinstack/internal/services/panelupdate"
)

var backupRecoverUnlessRestoreActive bool

var backupCmd = &cobra.Command{
	Use:    "backup",
	Short:  "Internal Panel backup restore and recovery commands",
	Hidden: true,
}

var backupRestoreCmd = &cobra.Command{
	Use:    "restore",
	Short:  "Consume the pending encrypted Panel restore request",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return errors.New("Panel restore requires root")
		}
		if _, err := app.LoadConfig(); err != nil {
			return fmt.Errorf("load current Panel configuration: %w", err)
		}
		manager, err := panelbackup.NewApplicationManager(nil)
		if err != nil {
			return err
		}
		request, err := manager.ConsumeRestoreRequest()
		if err != nil {
			return fmt.Errorf("consume Panel restore request: %w", err)
		}
		defer func() {
			request.Passphrase = ""
		}()
		startedAt := time.Now().UTC()
		writeStatus := func(status panelbackup.RestoreStatus) {
			if status.StartedAt == nil {
				status.StartedAt = &startedAt
			}
			status.UpdatedAt = time.Now().UTC()
			_ = manager.WriteStatus(status)
		}
		writeStatus(panelbackup.RestoreStatus{
			State: panelbackup.StatusValidating, BackupID: request.BackupID,
			Message: "正在重新校验加密备份包",
		})
		if _, err := manager.Preflight(cmd.Context(), request.BackupID, request.Passphrase); err != nil {
			writeStatus(failedRestoreStatus(request.BackupID, startedAt, err))
			return fmt.Errorf("panel backup restore preflight failed at %s", panelbackup.ValidationStageOf(err))
		}
		runner := panelupdate.OSCommandRunner{}
		writeStatus(panelbackup.RestoreStatus{
			State: panelbackup.StatusStopping, BackupID: request.BackupID,
			Message: "正在停止 Panel 服务并建立恢复事务",
		})
		if _, err := runner.Run(cmd.Context(), panelupdate.Command{
			Name: "systemctl", Args: []string{"stop", "one.service"},
		}); err != nil {
			writeStatus(failedRestoreStatus(request.BackupID, startedAt, err))
			return err
		}
		writeStatus(panelbackup.RestoreStatus{
			State: panelbackup.StatusRestoring, BackupID: request.BackupID,
			Message: "正在原子恢复配置、数据库、实例身份和证书",
		})
		transaction, err := manager.RestoreOffline(cmd.Context(), request.BackupID, request.Passphrase)
		request.Passphrase = ""
		if err != nil {
			_, _ = runner.Run(context.Background(), panelupdate.Command{
				Name: "systemctl", Args: []string{"start", "one.service"},
			})
			writeStatus(failedRestoreStatus(request.BackupID, startedAt, err))
			return err
		}
		writeStatus(panelbackup.RestoreStatus{
			State: panelbackup.StatusHealthCheck, BackupID: request.BackupID,
			Message: "恢复数据已切换，正在等待 Panel 健康检查",
		})
		startErr := func() error {
			if _, err := runner.Run(cmd.Context(), panelupdate.Command{
				Name: "systemctl", Args: []string{"start", "one.service"},
			}); err != nil {
				return err
			}
			healthURL, err := manager.HealthURL()
			if err != nil {
				return err
			}
			return (panelupdate.HTTPHealthChecker{}).WaitReady(cmd.Context(), healthURL, 90*time.Second)
		}()
		if startErr != nil {
			_, _ = runner.Run(context.Background(), panelupdate.Command{
				Name: "systemctl", Args: []string{"stop", "one.service"},
			})
			rollbackErr := transaction.Rollback()
			_, restartErr := runner.Run(context.Background(), panelupdate.Command{
				Name: "systemctl", Args: []string{"start", "one.service"},
			})
			finishedAt := time.Now().UTC()
			status := panelbackup.RestoreStatus{
				State: panelbackup.StatusRolledBack, BackupID: request.BackupID,
				Message:           "恢复后的 Panel 未通过健康检查，已恢复原配置和数据库",
				RollbackAttempted: true, RollbackSucceeded: rollbackErr == nil,
				StartedAt: &startedAt, UpdatedAt: finishedAt, FinishedAt: &finishedAt,
			}
			if rollbackErr != nil || restartErr != nil {
				status.State = panelbackup.StatusRollbackError
				status.Message = fmt.Sprintf(
					"恢复失败且自动回滚不完整：health=%v rollback=%v restart=%v",
					startErr,
					rollbackErr,
					restartErr,
				)
			}
			_ = manager.WriteStatus(status)
			return errors.Join(startErr, rollbackErr, restartErr)
		}
		if err := transaction.Commit(); err != nil {
			_, _ = runner.Run(context.Background(), panelupdate.Command{
				Name: "systemctl", Args: []string{"stop", "one.service"},
			})
			rollbackErr := transaction.Rollback()
			_, restartErr := runner.Run(context.Background(), panelupdate.Command{
				Name: "systemctl", Args: []string{"start", "one.service"},
			})
			finishedAt := time.Now().UTC()
			status := panelbackup.RestoreStatus{
				State: panelbackup.StatusRolledBack, BackupID: request.BackupID,
				Message:           "无法持久化恢复完成状态，已恢复原配置和数据库",
				RollbackAttempted: true, RollbackSucceeded: rollbackErr == nil,
				StartedAt: &startedAt, UpdatedAt: finishedAt, FinishedAt: &finishedAt,
			}
			if rollbackErr != nil || restartErr != nil {
				status.State = panelbackup.StatusRollbackError
				status.Message = fmt.Sprintf(
					"恢复提交失败且自动回滚不完整：commit=%v rollback=%v restart=%v",
					err,
					rollbackErr,
					restartErr,
				)
			}
			_ = manager.WriteStatus(status)
			return errors.Join(err, rollbackErr, restartErr)
		}
		finishedAt := time.Now().UTC()
		_ = manager.WriteStatus(panelbackup.RestoreStatus{
			State: panelbackup.StatusSucceeded, BackupID: request.BackupID,
			Message: "Panel 配置和数据恢复成功", StartedAt: &startedAt,
			UpdatedAt: finishedAt, FinishedAt: &finishedAt,
		})
		return nil
	},
}

var backupRecoverCmd = &cobra.Command{
	Use:    "recover",
	Short:  "Roll back an interrupted Panel restore transaction",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return errors.New("Panel restore recovery requires root")
		}
		if backupRecoverUnlessRestoreActive {
			runner := panelupdate.OSCommandRunner{}
			output, err := runner.Run(cmd.Context(), panelupdate.Command{
				Name: "systemctl",
				Args: []string{"show", "--property=ActiveState", "--value", "one-panel-restore.service"},
			})
			state := strings.TrimSpace(string(output))
			if err == nil && (state == "active" || state == "activating") {
				return nil
			}
		}
		// A crash can occur while config.yaml itself is between atomic rename
		// steps. Loading is best-effort here; the application factory falls
		// back to the default protected certificate directory so it can still
		// restore config.yaml and SQLite before one.service starts.
		_, _ = app.LoadConfig()
		manager, err := panelbackup.NewApplicationManager(nil)
		if err != nil {
			return err
		}
		return manager.RecoverInterruptedRestore()
	},
}

func configureBackupCommands() {
	backupRecoverCmd.Flags().BoolVar(
		&backupRecoverUnlessRestoreActive,
		"unless-restore-active",
		false,
		"Skip recovery while the managed restore service is active",
	)
	backupCmd.AddCommand(backupRestoreCmd, backupRecoverCmd)
}

func isPanelBackupCommand(command *cobra.Command) bool {
	for current := command; current != nil; current = current.Parent() {
		if current == backupCmd {
			return true
		}
	}
	return false
}

func failedRestoreStatus(backupID string, startedAt time.Time, cause error) panelbackup.RestoreStatus {
	finishedAt := time.Now().UTC()
	message := panelbackup.RestoreFailureMessage(cause)
	return panelbackup.RestoreStatus{
		State: panelbackup.StatusFailed, BackupID: backupID,
		Message: message, StartedAt: &startedAt,
		UpdatedAt: finishedAt, FinishedAt: &finishedAt,
	}
}
