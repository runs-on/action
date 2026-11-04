// Package gradle gives Gradle a remote build cache in the RunsOn agent's
// key/value tool cache: Gradle's HTTP build cache protocol is a GET and a PUT
// on <url>/<cache key>, which the agent serves as is.
package gradle

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// gradleUserHome is GRADLE_USER_HOME, or ~/.gradle.
func gradleUserHome() string {
	if home := os.Getenv("GRADLE_USER_HOME"); home != "" {
		return home
	}
	userHome, _ := os.UserHomeDir()
	return filepath.Join(userHome, ".gradle")
}

// initScriptPath is in Gradle's user home, whose init.d scripts every build
// runs.
func initScriptPath() string {
	return filepath.Join(gradleUserHome(), "init.d", "runs-on-build-cache.gradle")
}

// initScriptHeader starts the script, so the post step removes only its own.
const initScriptHeader = "// Written by runs-on/action (remote_cache: gradle) for this job"

// initScript makes the agent's cache the build's remote cache, unless the
// build configures an enabled remote cache of its own. Included builds run it
// too, and Gradle ignores their configuration in favor of the root build's,
// except for builds that provide plugins. It leaves the build cache off where
// the build has it off: turning it on here would also override
// --no-build-cache.
const initScript = initScriptHeader + `: a remote
// build cache stored by the RunsOn agent. The URL stands for the job's token:
// keep it out of logs.
if (!gradle.startParameter.buildCacheEnabled) {
    logger.lifecycle("runs-on/action: the build cache is off for this build, so it does not use the RunsOn cache. Turn it on with org.gradle.caching=true in gradle.properties, or --build-cache.")
}
gradle.settingsEvaluated { settings ->
    def remote = settings.buildCache.remote
    if (remote != null && remote.enabled) {
        logger.lifecycle("runs-on/action: this build configures its own remote build cache, which it uses instead of the RunsOn cache.")
        return
    }
    // A remote cache of the same type is configured in place, so a disabled
    // one must be turned on.
    settings.buildCache.remote(HttpBuildCache) {
        enabled = true
        url = '%s'
        push = true
        allowInsecureProtocol = true
    }
}
`

// Configure registers the job with the agent's tool caches and writes the
// init script that points Gradle at the gradle namespace of its key/value
// cache. When anything is missing, it warns and Gradle builds without the
// remote cache.
func Configure(action *githubactions.Action, magicCache bool, runtimeToken string) {
	const fallback = "Gradle builds without the remote cache"
	kvURL, ok := magiccache.RegisterFor(action, "remote_cache: gradle", "kv", fallback, magicCache, runtimeToken)
	if !ok {
		return
	}
	script := initScriptPath()
	if err := writeInitScript(script, kvURL+"/gradle/"); err != nil {
		action.Warningf("remote_cache: gradle could not write %s (%v); %s.", script, err, fallback)
		return
	}
	// Without GRADLE_USER_HOME, Gradle uses the JVM's user.home, which is the
	// account's home from the user database, not HOME: in a container job,
	// HOME is /github/home while root's home is /root. Exporting the home the
	// script is in makes Gradle read it there.
	if os.Getenv("GRADLE_USER_HOME") == "" {
		action.SetEnv("GRADLE_USER_HOME", gradleUserHome())
	}
	action.Infof("Wrote %s: Gradle builds in the next steps that have the build cache on use the RunsOn agent's cache, unless they configure a remote cache of their own.", script)
}

func writeInitScript(path, url string) error {
	if strings.ContainsAny(url, `'\`+"\n") {
		return fmt.Errorf("unexpected cache URL")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, fmt.Appendf(nil, initScript, url), 0o600)
}

// Finish runs in the action's post step: it removes the init script, whose
// URL stops working with the job's token, so later builds on the machine do
// not try it.
func Finish() {
	path := initScriptPath()
	content, err := os.ReadFile(path)
	if err == nil && bytes.HasPrefix(content, []byte(initScriptHeader)) {
		_ = os.Remove(path)
	}
}
