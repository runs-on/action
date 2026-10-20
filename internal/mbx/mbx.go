// Package mbx gives mbx (github.com/jdx/mr-boxington) a remote cache in the
// RunsOn agent's key/value tool cache. mbx's remote cache protocol is GET and
// PUT on paths under its base URL, which the agent serves as is: the
// capabilities document is missing, so mbx keeps to the baseline protocol,
// and mbx verifies the digest of every object it reads.
package mbx

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// Configure registers the job with the agent's tool caches and exports
// MBX_REMOTE_URL, a path per mbx namespace in the mbx namespace of its
// key/value cache, and, unless the job set one, MBX_REMOTE_NAMESPACE. mbx
// sends no token to it: the URL's path stands for the job's token, and mbx
// refuses a token over plain HTTP to another host. When anything is missing,
// it warns and exports nothing.
func Configure(action *githubactions.Action, mode string, magicCache bool, runtimeToken string) {
	const fallback = "mbx builds without the remote cache"
	if mode != "true" {
		action.Warningf("mbx is %q, want \"true\"; %s.", mode, fallback)
		return
	}
	if os.Getenv("MBX_REMOTE_TOKEN") != "" || os.Getenv("MBX_REMOTE_TOKEN_FILE") != "" || os.Getenv("MBX_REMOTE_OIDC_AUDIENCE") != "" {
		action.Warningf("the job sets mbx credentials (MBX_REMOTE_TOKEN, MBX_REMOTE_TOKEN_FILE or MBX_REMOTE_OIDC_AUDIENCE), which mbx refuses to send to the RunsOn agent over plain HTTP; %s.", fallback)
		return
	}
	kvURL, ok := magiccache.RegisterFor(action, "mbx", "kv", fallback, magicCache, runtimeToken)
	if !ok {
		return
	}
	namespace := os.Getenv("MBX_REMOTE_NAMESPACE")
	if namespace == "" {
		namespace = defaultNamespace()
		action.SetEnv("MBX_REMOTE_NAMESPACE", namespace)
	}
	// mbx sends its namespace in a header, which the key/value cache does not
	// read, so the URL keeps namespaces apart. A hash fits any namespace in
	// one path segment.
	sum := sha256.Sum256([]byte(namespace))
	action.SetEnv("MBX_REMOTE_URL", kvURL+"/mbx/"+hex.EncodeToString(sum[:16])+"/")
	action.Infof("Set MBX_REMOTE_URL: mbx uses the RunsOn agent's cache in the next steps.")
}

// defaultNamespace keeps the repository's objects apart: its ID survives a
// rename.
func defaultNamespace() string {
	if id := os.Getenv("GITHUB_REPOSITORY_ID"); id != "" {
		return id
	}
	return os.Getenv("GITHUB_REPOSITORY")
}
