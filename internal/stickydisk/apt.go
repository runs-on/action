package stickydisk

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

const (
	aptArchivesDir      = "/var/cache/apt/archives"
	aptConfigDir        = "/etc/apt/apt.conf.d"
	aptDockerCleanName  = "docker-clean"
	aptKeepArchivesName = "99runs-on-keep-archives"
	aptKeepArchivesConfig      = `APT::Keep-Downloaded-Packages "true";
Binary::apt::APT::Keep-Downloaded-Packages "true";
`
)

// configureApt makes the apt archives cache usable across jobs: apt requires
// the partial/ subdir to exist, and default image configs (docker-clean)
// delete downloaded packages after install.
func configureApt(action *githubactions.Action) error {
	return configureAptAt(action, aptArchivesDir, aptConfigDir)
}

func configureAptAt(action *githubactions.Action, archivesDir, configDir string) error {
	if err := runLogged(action, "sudo", "mkdir", "-p", filepath.Join(archivesDir, "partial")); err != nil {
		return err
	}
	dockerClean := filepath.Join(configDir, aptDockerCleanName)
	if _, err := os.Lstat(dockerClean); err == nil {
		// Park the file under RUNNER_TEMP for the duration of the job rather
		// than renaming it to docker-clean.disabled in place, which would
		// clobber a .disabled file the image or workflow already owns.
		backup := aptDockerCleanBackup()
		if err := runLogged(action, "sudo", "rm", "-f", "--", backup); err != nil {
			return err
		}
		if err := runLogged(action, "sudo", "mv", "--", dockerClean, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect apt configuration %s: %w", dockerClean, err)
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
	return restoreAptAt(action, aptConfigDir)
}

func restoreAptAt(action *githubactions.Action, configDir string) error {
	if err := runLogged(action, "sudo", "rm", "-f", "--", filepath.Join(configDir, aptKeepArchivesName)); err != nil {
		return err
	}
	backup := aptDockerCleanBackup()
	if _, err := os.Lstat(backup); err == nil {
		return runLogged(action, "sudo", "mv", "--", backup, filepath.Join(configDir, aptDockerCleanName))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect parked apt configuration %s: %w", backup, err)
	}
	return nil
}

// aptDockerCleanBackup is where the active docker-clean config is parked for
// the duration of the job; RUNNER_TEMP is wiped between jobs.
func aptDockerCleanBackup() string {
	return runnerTempPath("runs-on-apt-docker-clean")
}
