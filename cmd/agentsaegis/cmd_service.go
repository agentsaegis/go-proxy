package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var installServiceCmd = &cobra.Command{
	Use:   "install-service",
	Short: "Install AgentsAegis as a system service",
	RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("not implemented on this branch")
	},
}

var uninstallServiceCmd = &cobra.Command{
	Use:   "uninstall-service",
	Short: "Uninstall AgentsAegis system service",
	RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("not implemented on this branch")
	},
}

func init() {
	rootCmd.AddCommand(installServiceCmd)
	rootCmd.AddCommand(uninstallServiceCmd)
}

func launchdPlist(exePath, aegisDir string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.agentsaegis.proxy</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>start</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s/aegis.log</string>
  <key>StandardErrorPath</key>
  <string>%s/aegis.log</string>
</dict>
</plist>
`, exePath, aegisDir, aegisDir)
}

func systemdUnit(exePath, aegisDir string) string {
	return fmt.Sprintf(`[Unit]
Description=AgentsAegis Proxy
After=network.target

[Service]
ExecStart=%s start
Restart=on-failure
RestartSec=5
StandardOutput=append:%s/aegis.log
StandardError=append:%s/aegis.log

[Install]
WantedBy=default.target
`, exePath, aegisDir, aegisDir)
}
