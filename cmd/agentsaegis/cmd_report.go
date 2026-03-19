package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/spf13/cobra"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "View personal trap results and catch rate",
	RunE:  runReport,
}

func init() {
	rootCmd.AddCommand(reportCmd)
}

func runReport(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.APIToken == "" {
		fmt.Fprintln(os.Stderr, "Not connected to an organization. Run 'agentsaegis init' first.")
		return nil
	}

	apiClient := client.New(cfg.DashboardURL, cfg.APIToken)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stats, err := apiClient.FetchPersonalStats(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not fetch stats from dashboard: %v\n", err)
		fmt.Fprintln(os.Stderr, "Showing local session info only.")
		fmt.Println()
		fmt.Println("  No trap data available. Stats require a connection to the dashboard.")
		return nil
	}

	fmt.Println()
	fmt.Println("  AgentsAegis - Personal Report")
	fmt.Println("  ──────────────────────────────")
	fmt.Println()
	fmt.Printf("  Catch Rate:    %s\n", stats.CatchRate)
	fmt.Printf("  Traps Faced:   %d\n", stats.TotalTraps)
	fmt.Printf("  Caught:        %d\n", stats.Caught)
	fmt.Printf("  Missed:        %d\n", stats.Missed)
	fmt.Println()

	if len(stats.RecentTraps) > 0 {
		fmt.Println("  Recent Traps:")
		for _, trap := range stats.RecentTraps {
			icon := "\033[32m+\033[0m"
			if trap.Result == "missed" {
				icon = "\033[31mx\033[0m"
			}
			fmt.Printf("    %s  %-30s  %s  %s\n", icon, trap.Category, trap.Result, trap.Date)
		}
		fmt.Println()
	}

	return nil
}
