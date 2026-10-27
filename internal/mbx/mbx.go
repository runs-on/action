// Package mbx gives mbx (github.com/jdx/mr-boxington) a remote cache in the
// RunsOn agent's key/value tool cache. mbx's remote cache protocol is GET and
// PUT on paths under its base URL, which the agent serves as is: the
// capabilities document is missing, so mbx keeps to the baseline protocol,
// and mbx verifies the digest of every object it reads.
package mbx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// Configure registers the job with the agent's remote caches and exports
// MBX_REMOTE_URL, a path per mbx namespace in the mbx namespace of its
// key/value cache, and, unless the job set one, MBX_REMOTE_NAMESPACE. mbx
// sends no token to it: the URL's path stands for the job's token, and mbx
// refuses a token over plain HTTP to another host. When anything is missing,
// it warns and exports nothing.
func Configure(action *githubactions.Action, magicCache bool, runtimeToken string) {
	const fallback = "mbx builds without the remote cache"
	if os.Getenv("MBX_REMOTE_TOKEN") != "" || os.Getenv("MBX_REMOTE_TOKEN_FILE") != "" || os.Getenv("MBX_REMOTE_OIDC_AUDIENCE") != "" {
		action.Warningf("the job sets mbx credentials (MBX_REMOTE_TOKEN, MBX_REMOTE_TOKEN_FILE or MBX_REMOTE_OIDC_AUDIENCE), which mbx refuses to send to the RunsOn agent over plain HTTP; %s.", fallback)
		return
	}
	// mbx refuses S3 settings with a URL that is not a bucket's.
	if set := s3Settings(); len(set) > 0 {
		action.Warningf("the job sets mbx's S3 settings (%s), which mbx refuses with the RunsOn agent's HTTP cache; the action leaves mbx's remote alone.", strings.Join(set, ", "))
		return
	}
	kvURL, ok := magiccache.RegisterFor(action, "remote_cache: mbx", "kv", fallback, magicCache, runtimeToken)
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
	remoteURL := kvURL + "/mbx/" + hex.EncodeToString(sum[:16]) + "/"
	action.SetEnv("MBX_REMOTE_URL", remoteURL)
	action.Infof("Set MBX_REMOTE_URL: mbx uses the RunsOn agent's cache in the next steps.")
	if err := storeCapabilities(remoteURL); err != nil {
		action.Infof("Could not store mbx's capabilities document (%v): mbx builds work without it, but mbx's setup action warns that it could not reach the remote cache.", err)
	}
}

// capabilities is mbx's protocol 1 capabilities document claiming no
// optional feature, which is what mbx builds assume when there is none. mbx
// doctor, which mbx's setup action runs, requires one.
const capabilities = `{"protocol":{"major":1,"minor":0}}`

// storeCapabilities stores the capabilities document in the cache, where mbx
// reads it: it is an ordinary entry, which the job rewrites in its own scope.
func storeCapabilities(remoteURL string) error {
	req, err := http.NewRequest(http.MethodPut, remoteURL+"v1/capabilities", strings.NewReader(capabilities))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the agent answered HTTP %d", resp.StatusCode)
	}
	return nil
}

// defaultNamespace keeps the repository's objects apart: its ID survives a
// rename.
func defaultNamespace() string {
	if id := os.Getenv("GITHUB_REPOSITORY_ID"); id != "" {
		return id
	}
	return os.Getenv("GITHUB_REPOSITORY")
}

// s3Settings are the MBX_REMOTE_S3_* variables the step's environment sets.
func s3Settings() []string {
	var set []string
	for _, variable := range os.Environ() {
		name, value, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "MBX_REMOTE_S3_") && value != "" {
			set = append(set, name)
		}
	}
	slices.Sort(set)
	return set
}
