package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/legacyoneinstack"
	softwareHandler "oneinstack/router/handler/software"

	"github.com/spf13/cobra"
)

var legacySourceDir string
var legacyCmd = &cobra.Command{Use: "compat", Short: "Discover and adopt existing OneinStack script environments"}
var legacyScanCmd = &cobra.Command{Use: "scan", Short: "Read the existing environment without changing it", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
	defer cancel()
	report, err := legacyoneinstack.Scan(ctx, legacySourceDir)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}}
var legacyImportCmd = &cobra.Command{Use: "import", Short: "Register existing resources without reinstalling or rewriting their configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
	if isServerRunning() {
		return errors.New("Panel is running; use POST /v1/soft/compat/oneinstack/import to refresh through its active task manager")
	}
	var admin models.User
	if err := app.DB().Where("is_admin = ?", true).Order("id ASC").First(&admin).Error; err != nil {
		return errors.New("initialize a Panel administrator before importing")
	}
	m, err := softwareHandler.GetTaskManagerForLocalLifecycle()
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Stop(ctx)
	}()
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	task, err := m.SubmitLegacyImport(ctx, legacySourceDir, admin.ID)
	if err != nil {
		return err
	}
	if task == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "No existing OneinStack environment detected.")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "OneinStack adoption task: %s\n", task.ID)
	for !models.IsSoftwareTaskTerminal(task.Status) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
			task, err = m.Get(task.ID)
			if err != nil {
				return err
			}
		}
	}
	if task.Status != models.SoftwareTaskStatusSucceeded {
		return fmt.Errorf("adoption failed: %s", task.ErrorCode)
	}
	fmt.Fprintln(cmd.OutOrStdout(), task.Message)
	return nil
}}

func configureLegacyCommands() {
	legacyCmd.PersistentFlags().StringVar(&legacySourceDir, "source-dir", "", "OneinStack script directory; normally detected automatically")
	legacyCmd.AddCommand(legacyScanCmd, legacyImportCmd)
	rootCmd.AddCommand(legacyCmd)
}
