package kopia

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func (c *KopiaClient) preSnapshot(ctx context.Context, directory string) error {
	if strings.HasPrefix(directory, "/var/lib/docker") {
		return stopDocker(ctx)
	}
	return nil
}

func (c *KopiaClient) postSnapshot(ctx context.Context, directory string) error {
	return nil
}

func (c *KopiaClient) preRestore(ctx context.Context, directory string) error {
	if strings.HasPrefix(directory, "/var/lib/docker") {
		return stopDocker(ctx)
	}
	return nil
}

func (c *KopiaClient) postRestore(ctx context.Context, directory string) error {
	if strings.HasPrefix(directory, "/var/lib/docker") {
		return startDocker(ctx)
	}
	return nil
}

func startDocker(ctx context.Context) error {
	if err := exec.CommandContext(ctx, "sudo", "systemctl", "start", "docker").Run(); err != nil {
		return fmt.Errorf("failed to start docker: %w", err)
	}
	return nil
}
func stopDocker(ctx context.Context) error {
	if err := exec.CommandContext(ctx, "sudo", "systemctl", "stop", "docker").Run(); err != nil {
		return fmt.Errorf("failed to stop docker: %w", err)
	}
	return nil
}
