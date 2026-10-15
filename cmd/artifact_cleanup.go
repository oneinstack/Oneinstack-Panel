package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"oneinstack/app"
	"oneinstack/internal/services/artifactcleanup"
	"oneinstack/internal/services/audit"
	"oneinstack/utils"

	"github.com/spf13/cobra"
)

var artifactCleanupID string
var artifactCleanupConfirmation string

var artifactsCmd = &cobra.Command{Use: "artifacts", Short: "Inspect Panel generated files and cleanup tasks"}

var artifactsListCmd = &cobra.Command{
	Use: "list", Short: "List fixed cleanup candidates", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		inventory, err := artifactcleanup.List(time.Now())
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(inventory)
	},
}

var artifactsCleanCmd = &cobra.Command{
	Use: "clean", Short: "Remove one reviewed candidate by ID", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return errors.New("artifact cleanup requires root")
		}
		if len(artifactCleanupID) != 64 || artifactCleanupConfirmation != artifactCleanupID {
			return errors.New("provide --id and repeat the same candidate ID with --confirm")
		}
		manager, err := artifactcleanup.Default(app.DB())
		if err != nil {
			return err
		}
		auditKey, err := utils.DeriveCredentialSubkey("audit-log-hmac-v1")
		if err != nil {
			return err
		}
		auditManager, err := audit.ConfigureDefault(app.DB(), auditKey)
		if err != nil {
			return err
		}
		defer audit.ClearDefault(auditManager)
		task, err := manager.RunCLI(artifactCleanupID)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(task); err != nil {
			return err
		}
		if task.Status != "succeeded" {
			return fmt.Errorf("cleanup task %s ended with %s: %s", task.ID, task.Status, task.ErrorCode)
		}
		return nil
	},
}

func configureArtifactCommands() {
	artifactsCleanCmd.Flags().StringVar(&artifactCleanupID, "id", "", "Candidate ID from artifacts list")
	artifactsCleanCmd.Flags().StringVar(&artifactCleanupConfirmation, "confirm", "", "Repeat the candidate ID to confirm irreversible deletion")
	artifactsCmd.AddCommand(artifactsListCmd, artifactsCleanCmd)
}
