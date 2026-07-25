package stickydisk

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

const (
	aptArchivesDir             = "/var/cache/apt/archives"
	aptConfigDir               = "/etc/apt/apt.conf.d"
	aptDockerCleanName         = "docker-clean"
	aptKeepArchivesName        = "99runs-on-keep-archives"
	aptConfigSnapshotStateKey  = "runs_on_apt_config_snapshot"
	aptConfigSnapshotStateEnv  = "STATE_" + aptConfigSnapshotStateKey
	aptConfigSnapshotDirPrefix = "runs-on-apt-config-"
	aptKeepArchivesConfig      = `APT::Keep-Downloaded-Packages "true";
Binary::apt::APT::Keep-Downloaded-Packages "true";
`
)

// configureApt makes the apt archives cache usable across jobs: apt requires
// the partial/ subdir to exist, and default image configs (docker-clean)
// delete downloaded packages after install.
func configureApt(action *githubactions.Action) error {
	return configureAptAt(action, aptArchivesDir, aptConfigDir, aptStateTempDir())
}

func aptStateTempDir() string {
	if dir := os.Getenv("RUNNER_TEMP"); dir != "" {
		return dir
	}
	return os.TempDir()
}

func configureAptAt(action *githubactions.Action, archivesDir, configDir, stateTempDir string) (retErr error) {
	snapshotRoot, err := os.MkdirTemp(stateTempDir, aptConfigSnapshotDirPrefix)
	if err != nil {
		return fmt.Errorf("create apt configuration snapshot directory: %w", err)
	}
	snapshotSaved := false
	defer func() {
		if retErr == nil || snapshotSaved {
			return
		}
		if cleanupErr := runLogged(action, "sudo", "rm", "-rf", "--", snapshotRoot); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove incomplete apt configuration snapshot %s: %w", snapshotRoot, cleanupErr))
		}
	}()

	// The runner may reuse the host for another job, so preserve both files
	// changed below and let the post action restore their exact prior state.
	for _, name := range []string{aptDockerCleanName, aptKeepArchivesName} {
		source := filepath.Join(configDir, name)
		if _, err := os.Lstat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect apt configuration %s: %w", source, err)
		}
		if err := runLogged(action, "sudo", "cp", "-a", "--", source, filepath.Join(snapshotRoot, name)); err != nil {
			return fmt.Errorf("snapshot apt configuration %s: %w", source, err)
		}
	}
	action.SaveState(aptConfigSnapshotStateKey, snapshotRoot)
	snapshotSaved = true

	if err := runLogged(action, "sudo", "mkdir", "-p", filepath.Join(archivesDir, "partial")); err != nil {
		return err
	}
	if err := runLogged(action, "sudo", "rm", "-f", "--", filepath.Join(configDir, aptDockerCleanName)); err != nil {
		return err
	}

	keepArchivesPath := filepath.Join(configDir, aptKeepArchivesName)
	action.Infof("Writing %s", keepArchivesPath)
	cmd := exec.Command("sudo", "tee", keepArchivesPath)
	cmd.Stdin = strings.NewReader(aptKeepArchivesConfig)
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func restoreApt(action *githubactions.Action) error {
	return restoreAptAt(action, aptConfigDir, aptStateTempDir())
}

func restoreAptAt(action *githubactions.Action, configDir, stateTempDir string) error {
	snapshotRoot := os.Getenv(aptConfigSnapshotStateEnv)
	if snapshotRoot == "" {
		return nil
	}
	cleanSnapshotRoot := filepath.Clean(snapshotRoot)
	if filepath.Dir(cleanSnapshotRoot) != filepath.Clean(stateTempDir) ||
		!strings.HasPrefix(filepath.Base(cleanSnapshotRoot), aptConfigSnapshotDirPrefix) {
		return fmt.Errorf("refusing unsafe apt configuration snapshot path %s", snapshotRoot)
	}
	info, err := os.Lstat(cleanSnapshotRoot)
	if err != nil {
		return fmt.Errorf("inspect apt configuration snapshot %s: %w", cleanSnapshotRoot, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("apt configuration snapshot %s is not a directory", cleanSnapshotRoot)
	}

	// Restore each global apt setting before the runner can be reused. An
	// absent snapshot means the action created the setting and must remove it.
	var restoreErr error
	for _, name := range []string{aptDockerCleanName, aptKeepArchivesName} {
		snapshot := filepath.Join(cleanSnapshotRoot, name)
		target := filepath.Join(configDir, name)
		snapshotInfo, err := os.Lstat(snapshot)
		if os.IsNotExist(err) {
			if err := runLogged(action, "sudo", "rm", "-f", "--", target); err != nil {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("remove action apt configuration %s: %w", target, err))
			}
			continue
		} else if err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("inspect apt configuration snapshot %s: %w", snapshot, err))
			continue
		}
		if snapshotInfo.IsDir() {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("apt configuration snapshot %s is a directory", snapshot))
			continue
		}
		if err := runLogged(action, "sudo", "rm", "-f", "--", target); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("prepare apt configuration restore %s: %w", target, err))
			continue
		}
		if err := runLogged(action, "sudo", "cp", "-a", "--", snapshot, target); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore apt configuration %s: %w", target, err))
		}
	}
	if restoreErr != nil {
		return restoreErr
	}

	// The path is constrained to the exact temp parent and prefix above before
	// this privileged recursive removal.
	if err := runLogged(action, "sudo", "rm", "-rf", "--", cleanSnapshotRoot); err != nil {
		return fmt.Errorf("remove apt configuration snapshot %s: %w", cleanSnapshotRoot, err)
	}
	return nil
}
