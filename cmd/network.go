package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	systemService "oneinstack/internal/services/system"

	"github.com/spf13/cobra"
)

var networkTransactionID string

var networkCmd = &cobra.Command{
	Use:    "network",
	Short:  "Internal panel network transaction commands",
	Hidden: true,
}

var networkApplyCmd = &cobra.Command{
	Use:    "apply",
	Short:  "Apply and verify a pending panel network transaction",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return errors.New("panel network transactions require root")
		}
		if networkTransactionID == "" {
			return errors.New("--transaction is required")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
		defer cancel()
		if err := systemService.ApplyPanelNetworkTransaction(ctx, networkTransactionID); err != nil {
			return fmt.Errorf("apply panel network transaction: %w", err)
		}
		return nil
	},
}

var networkRecoverCmd = &cobra.Command{
	Use:    "recover",
	Short:  "Recover the latest incomplete panel network transaction",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return errors.New("panel network recovery requires root")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
		defer cancel()
		if err := systemService.RecoverPendingPanelNetworkTransaction(ctx); err != nil {
			return fmt.Errorf("recover panel network transaction: %w", err)
		}
		return nil
	},
}

func configureNetworkCommands() {
	networkApplyCmd.Flags().StringVar(
		&networkTransactionID,
		"transaction",
		"",
		"pending panel network transaction id",
	)
	networkCmd.AddCommand(networkApplyCmd)
	networkCmd.AddCommand(networkRecoverCmd)
}

func isNetworkTransactionCommand(cmd *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current == networkCmd {
			return true
		}
	}
	return false
}
