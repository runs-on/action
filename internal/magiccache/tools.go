package magiccache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/sethvargo/go-githubactions"
)

// ToolsPath is where the agent lists the tool cache protocols it serves
// (GET) and registers a job's runtime token for them (POST).
const ToolsPath = "/_runs-on/tools"

// ToolsKVURLEnv names the job variable that holds the URL of the agent's
// key/value tool cache, for clients a step configures itself, such as
// <URL>/<namespace>/ for a plain HTTP cache.
const ToolsKVURLEnv = "RUNS_ON_TOOLS_KV_URL"

// ErrToolsNotServed is the registration's error on agents that do not serve
// the tool caches.
var ErrToolsNotServed = errors.New("this RunsOn agent does not serve the tool caches; upgrade it")

// Tools is a job's registration: the URL of each tool cache protocol the
// agent serves, and the ID in those URLs, which stands for the job's runtime
// token and is therefore masked like a secret.
type Tools struct {
	ID   string
	URLs map[string]string
}

// noRedirect keeps requests to the agent from following a redirect elsewhere.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func agentURL() (*url.URL, error) {
	base, err := url.Parse(URL())
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return nil, fmt.Errorf("invalid Magic Cache URL")
	}
	return &url.URL{Scheme: base.Scheme, Host: base.Host}, nil
}

// validID matches the IDs the agent derives from tokens: long enough that
// masking one hides nothing else in the job's logs.
var validID = regexp.MustCompile(`^[0-9a-f]{32,128}$`)

// validToolPath matches the paths the agent gives protocols: absolute paths
// on the agent itself, which go into tool configurations as they are, so
// with no escaping, query, or segment that starts with a dot.
var validToolPath = regexp.MustCompile(`^(/[A-Za-z0-9_~-][A-Za-z0-9._~-]*)+$`)

// Register exchanges the job's runtime token for the URLs of its tool caches.
// Only the agent keeps the token: the URLs hold an ID the agent derives from
// it, which works only on this agent, for the token's scopes, until it
// expires. Registering the same token again returns the same URLs. The agent
// chooses each protocol's path; they stay on the agent the action posted to.
func Register(runtimeToken string) (Tools, error) {
	agent, err := agentURL()
	if err != nil {
		return Tools{}, err
	}
	req, err := http.NewRequest(http.MethodPost, agent.String()+ToolsPath, nil)
	if err != nil {
		return Tools{}, err
	}
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return Tools{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return Tools{}, ErrToolsNotServed
	}
	if resp.StatusCode != http.StatusOK {
		return Tools{}, fmt.Errorf("the agent answered the registration with HTTP %d", resp.StatusCode)
	}
	var registration struct {
		ID        string            `json:"id"`
		Protocols map[string]string `json:"protocols"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&registration); err != nil {
		return Tools{}, fmt.Errorf("invalid registration response")
	}
	// The ID and the paths hold a credential: malformed ones are refused
	// without being logged.
	if !validID.MatchString(registration.ID) {
		return Tools{}, fmt.Errorf("invalid registration ID")
	}
	tools := Tools{ID: registration.ID, URLs: make(map[string]string, len(registration.Protocols))}
	for protocol, path := range registration.Protocols {
		if !validToolPath.MatchString(path) {
			return Tools{}, fmt.Errorf("invalid registration path for %q", protocol)
		}
		tools.URLs[protocol] = agent.String() + path
	}
	return tools, nil
}

// Serves reports whether the runner's agent serves a tool cache protocol, from
// the list it gives without a token. Agents before the tool caches answer 404.
func Serves(protocol string) bool {
	agent, err := agentURL()
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: noRedirect}
	resp, err := client.Get(agent.String() + ToolsPath)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	var capabilities struct {
		Protocols []string `json:"protocols"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&capabilities) != nil {
		return false
	}
	return slices.Contains(capabilities.Protocols, protocol)
}

// registrations keeps, per agent and runtime token, the outcome of this process's
// registration: the tool inputs of one action step share it, so an agent
// that stops answering costs one timeout, not one per tool.
var registrations = struct {
	sync.Mutex
	byToken map[string]registration
}{byToken: map[string]registration{}}

type registration struct {
	tools Tools
	err   error
}

func registerOnce(runtimeToken string) (Tools, error) {
	key := URL() + "\x00" + runtimeToken
	registrations.Lock()
	defer registrations.Unlock()
	if done, ok := registrations.byToken[key]; ok {
		return done.tools, done.err
	}
	tools, err := Register(runtimeToken)
	registrations.byToken[key] = registration{tools, err}
	return tools, err
}

// RegisterFor registers the job for a tool cache, or warns why it cannot, and
// returns the URL of the protocol it configures. input names the action
// input, protocol the one it configures, which the registration must give a
// URL, and fallback says what the job does instead, for example "Bazel builds
// without the remote cache". The ID is masked in the job's logs: it stands
// for the job's runtime token. Later steps get the key/value cache's URL as
// RUNS_ON_TOOLS_KV_URL.
func RegisterFor(action *githubactions.Action, input, protocol, fallback string, magicCache bool, runtimeToken string) (string, bool) {
	if !magicCache {
		action.Warningf("%s needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); %s.", input, fallback)
		return "", false
	}
	if runtimeToken == "" {
		action.Warningf("%s needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; %s.", input, fallback)
		return "", false
	}
	tools, err := registerOnce(runtimeToken)
	if errors.Is(err, ErrToolsNotServed) {
		action.Warningf("%s needs RunsOn v3.6.0 or later, whose agent serves the tool caches; %s.", input, fallback)
		return "", false
	}
	if err != nil {
		action.Warningf("%s could not register the job with the RunsOn agent (%v); %s.", input, err, fallback)
		return "", false
	}
	action.AddMask(tools.ID)
	toolURL, ok := tools.URLs[protocol]
	if !ok {
		action.Warningf("%s needs a newer RunsOn version, whose agent serves the %q tool cache; %s.", input, protocol, fallback)
		return "", false
	}
	if kv := tools.URLs["kv"]; kv != "" {
		action.SetEnv(ToolsKVURLEnv, kv)
		// This step's own children, such as gocacheprog's prefetch, see it too.
		_ = os.Setenv(ToolsKVURLEnv, kv)
	}
	return toolURL, true
}
