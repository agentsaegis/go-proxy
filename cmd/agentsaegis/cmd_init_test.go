package main

import (
	"testing"
)

func TestInitCommand_Registered(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "init" {
			found = true
			break
		}
	}
	if !found {
		t.Error("init command not registered on root")
	}
}
