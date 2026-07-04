package stickydisk

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sethvargo/go-githubactions"
)

const (
	buildkitVersion     = "v0.31.1"
	buildkitSocketDir   = "/run/runs-on"
	buildkitSocketPath  = buildkitSocketDir + "/buildkitd.sock"
	buildkitBuilderName = "runs-on"
	buildkitLogFile     = "/tmp/buildkitd.log"
	buildkitStartWait   = 30 * time.Second
	buildkitStopWait    = 20 * time.Second
)

func buildkitTarballURL(version, arch string) string {
	return fmt.Sprintf("https://github.com/moby/buildkit/releases/download/%s/buildkit-%s.linux-%s.tar.gz", version, version, arch)
}

// setupBuildkit runs a dedicated buildkitd daemon whose state lives on the
// sticky disk, and registers it as the current buildx builder. The docker
// daemon is never touched: only buildx builds are accelerated, and images
// need `--load` to end up in the local daemon.
func setupBuildkit(action *githubactions.Action, mountRoot string) (hit bool, err error) {
	if err := exec.Command("docker", "buildx", "version").Run(); err != nil {
		return false, fmt.Errorf("docker buildx is not available: %w", err)
	}

	binDir := filepath.Join(mountRoot, "buildkit", "bin")
	stateRoot := filepath.Join(mountRoot, "buildkit", "root")
	hit = dirNonEmpty(stateRoot)

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return hit, fmt.Errorf("failed to create %s: %w", binDir, err)
	}
	if err := os.MkdirAll(stateRoot, 0755); err != nil {
		return hit, fmt.Errorf("failed to create %s: %w", stateRoot, err)
	}

	// Download buildkit binaries once per lineage: they are cached on the
	// sticky disk itself, so warm jobs download nothing.
	buildkitd := filepath.Join(binDir, "buildkitd")
	if _, statErr := os.Stat(buildkitd); os.IsNotExist(statErr) {
		if err := downloadBuildkit(action, binDir); err != nil {
			return hit, err
		}
	} else {
		action.Infof("Using buildkit binaries cached on sticky disk (%s)", binDir)
	}

	if err := runLogged(action, "sudo", "mkdir", "-p", buildkitSocketDir); err != nil {
		return hit, err
	}

	// Start buildkitd detached, state on the sticky disk. --group docker
	// makes the socket accessible to the runner user (member of docker group).
	action.Infof("Starting buildkitd %s (root: %s)", buildkitVersion, stateRoot)
	startCmd := fmt.Sprintf("nohup %s --root %s --addr unix://%s --group docker > %s 2>&1 &",
		buildkitd, stateRoot, buildkitSocketPath, buildkitLogFile)
	if err := runLogged(action, "sudo", "sh", "-c", startCmd); err != nil {
		return hit, err
	}

	if err := waitForBuildkit(buildkitStartWait); err != nil {
		if log, readErr := os.ReadFile(buildkitLogFile); readErr == nil {
			action.Errorf("buildkitd log:\n%s", tailString(string(log), 2000))
		}
		return hit, err
	}

	// Register (or replace) the buildx builder and make it the current one.
	_ = exec.Command("docker", "buildx", "rm", "--force", buildkitBuilderName).Run()
	if err := runLogged(action, "docker", "buildx", "create",
		"--name", buildkitBuilderName,
		"--driver", "remote",
		"--use",
		"unix://"+buildkitSocketPath); err != nil {
		return hit, err
	}

	action.Infof("Buildx builder '%s' ready (remote buildkitd on sticky disk). Use `docker buildx build` (add --load to get images into the local docker daemon).", buildkitBuilderName)
	return hit, nil
}

// downloadBuildkit fetches the pinned buildkit release tarball and extracts
// buildkitd + buildctl into binDir.
func downloadBuildkit(action *githubactions.Action, binDir string) error {
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported architecture for buildkit: %s", arch)
	}
	url := buildkitTarballURL(buildkitVersion, arch)
	tarball := filepath.Join(os.TempDir(), "buildkit.tar.gz")
	action.Infof("Downloading %s", url)
	if err := runLogged(action, "curl", "-fsSL", "-o", tarball, url); err != nil {
		return err
	}
	defer os.Remove(tarball)
	// Tarball layout: bin/buildkitd, bin/buildctl, bin/buildkit-runc, ...
	return runLogged(action, "tar", "-xzf", tarball, "-C", binDir, "--strip-components=1", "bin/buildkitd", "bin/buildctl")
}

// waitForBuildkit polls the buildkitd unix socket until it accepts
// connections or the timeout elapses.
func waitForBuildkit(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("unix", buildkitSocketPath, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("buildkitd did not become ready within %s (socket: %s)", timeout, buildkitSocketPath)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// stopBuildkit gracefully stops buildkitd so its state is consistent before
// the sticky disk is unmounted and snapshotted.
func stopBuildkit(action *githubactions.Action) error {
	if exec.Command("pgrep", "-x", "buildkitd").Run() != nil {
		return nil
	}
	action.Infof("Stopping buildkitd before sticky disk snapshot...")
	if err := runLogged(action, "sudo", "pkill", "-TERM", "-x", "buildkitd"); err != nil {
		return err
	}
	deadline := time.Now().Add(buildkitStopWait)
	for time.Now().Before(deadline) {
		if exec.Command("pgrep", "-x", "buildkitd").Run() != nil {
			action.Infof("buildkitd stopped.")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	action.Warningf("buildkitd did not stop within %s, killing it", buildkitStopWait)
	return runLogged(action, "sudo", "pkill", "-KILL", "-x", "buildkitd")
}

// tailString returns at most the last n bytes of s.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
