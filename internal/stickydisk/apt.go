package stickydisk

import (
	"os"
	"os/exec"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

const aptKeepArchivesConfig = `APT::Keep-Downloaded-Packages "true";
Binary::apt::APT::Keep-Downloaded-Packages "true";
`

// configureApt makes the apt archives cache usable across jobs: apt requires
// the partial/ subdir to exist, and default image configs (docker-clean)
// delete downloaded packages after install.
func configureApt(action *githubactions.Action) error {
	if err := runLogged(action, "sudo", "mkdir", "-p", "/var/cache/apt/archives/partial"); err != nil {
		return err
	}
	if err := runLogged(action, "sudo", "rm", "-f", "/etc/apt/apt.conf.d/docker-clean"); err != nil {
		return err
	}

	action.Infof("Writing /etc/apt/apt.conf.d/99runs-on-keep-archives")
	cmd := exec.Command("sudo", "tee", "/etc/apt/apt.conf.d/99runs-on-keep-archives")
	cmd.Stdin = strings.NewReader(aptKeepArchivesConfig)
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
