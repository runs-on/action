package mbx

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestNamespaceIsTheRepositoryID(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "123456789")

	namespace, err := Namespace()
	if err != nil {
		t.Fatal(err)
	}
	if namespace != "123456789" {
		t.Fatalf("Namespace() = %q, want the repository ID", namespace)
	}
}

func TestNamespaceRejectsAMissingOrNonNumericID(t *testing.T) {
	for _, id := range []string{"", "12a", "acme/project"} {
		t.Run(id, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY_ID", id)
			if namespace, err := Namespace(); err == nil {
				t.Fatalf("Namespace() = %q, want an error", namespace)
			}
		})
	}
}

// configure runs ConfigureMbx with any extra variables as name, value pairs,
// and returns what it wrote to GITHUB_ENV and to the log.
func configure(t *testing.T, backend string, extra ...string) (string, string, error) {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "github-env")
	t.Setenv("GITHUB_ENV", envFile)
	t.Setenv("RUNS_ON_S3_BUCKET_CACHE", "runs-on-cache")
	t.Setenv("RUNS_ON_AWS_REGION", "eu-west-1")
	t.Setenv("GITHUB_REPOSITORY_ID", "123456789")
	for _, name := range credentialSources {
		t.Setenv(name, "")
	}
	for i := 0; i+1 < len(extra); i += 2 {
		t.Setenv(extra[i], extra[i+1])
	}

	var log bytes.Buffer
	err := ConfigureMbx(githubactions.New(githubactions.WithWriter(&log)), backend)
	data, readErr := os.ReadFile(envFile)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	return string(data), log.String(), err
}

func TestConfigureExportsTheRemoteOnly(t *testing.T) {
	env, log, err := configure(t, "s3")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"MBX_REMOTE_URL", "s3://runs-on-cache/cache/mbx",
		"MBX_REMOTE_NAMESPACE", "123456789",
		"MBX_REMOTE_S3_REGION", "eu-west-1",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("GITHUB_ENV is missing %q:\n%s", want, env)
		}
	}
	// mbx signs with the instance role itself. AWS_* here would win over it,
	// stop renewing, and replace the instance profile for later steps.
	if strings.Contains(env, "AWS_") {
		t.Errorf("GITHUB_ENV exports AWS variables:\n%s", env)
	}
	// mbx defaults to read-write, and leaving the mode unset keeps one an
	// earlier step or the job's env chose.
	if strings.Contains(env, "MBX_REMOTE_MODE") {
		t.Errorf("GITHUB_ENV sets MBX_REMOTE_MODE:\n%s", env)
	}
	if strings.Contains(log, "::warning") {
		t.Errorf("a valid configuration produced a warning:\n%s", log)
	}
}

func TestConfigureWarnsAboutOtherCredentialSources(t *testing.T) {
	for _, name := range credentialSources {
		t.Run(name, func(t *testing.T) {
			env, log, err := configure(t, "s3", name, "set-by-an-earlier-step")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(log, "::warning::"+name+" is set") {
				t.Fatalf("%s produced no warning:\n%s", name, log)
			}
			if !strings.Contains(env, "MBX_REMOTE_URL") {
				t.Fatalf("the remote was not exported:\n%s", env)
			}
		})
	}
}

func TestConfigureExportsNothingWhenAValueIsMissing(t *testing.T) {
	for _, name := range []string{"RUNS_ON_S3_BUCKET_CACHE", "RUNS_ON_AWS_REGION", "GITHUB_REPOSITORY_ID"} {
		t.Run(name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), "github-env")
			t.Setenv("GITHUB_ENV", envFile)
			t.Setenv("RUNS_ON_S3_BUCKET_CACHE", "runs-on-cache")
			t.Setenv("RUNS_ON_AWS_REGION", "eu-west-1")
			t.Setenv("GITHUB_REPOSITORY_ID", "123456789")
			t.Setenv(name, "")

			var log bytes.Buffer
			if err := ConfigureMbx(githubactions.New(githubactions.WithWriter(&log)), "s3"); err == nil {
				t.Fatalf("ConfigureMbx succeeded without %s", name)
			}
			if data, err := os.ReadFile(envFile); err == nil && len(data) > 0 {
				t.Fatalf("a failed configuration exported variables:\n%s", data)
			}
		})
	}
}

func TestConfigureIgnoresUnsupportedBackends(t *testing.T) {
	env, log, err := configure(t, "gha")
	if err != nil {
		t.Fatal(err)
	}
	if env != "" {
		t.Fatalf("an unsupported backend exported variables:\n%s", env)
	}
	if !strings.Contains(log, "Unsupported mbx backend") {
		t.Fatalf("an unsupported backend was not reported:\n%s", log)
	}
}
