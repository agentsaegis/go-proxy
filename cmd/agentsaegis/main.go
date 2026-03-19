// Package main is the entry point for the AgentsAegis CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "agentsaegis",
	Short:   "AgentsAegis - AI security awareness testing for developers",
	Long:    "AgentsAegis tests whether developers read what they approve from AI coding agents.",
	Version: version,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
