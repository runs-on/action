package costs

import (
	"os"
	"path/filepath"
	"strings"
)

// AgentModeFileNameEnv is set by RunsOn agents (v3.4.0+) that report the job
// cost themselves, from their job-completed hook. Its value names the file in
// RUNNER_TEMP where the action asks how the cost is displayed. RUNNER_TEMP is
// shared with job containers and the host hook, unlike the agent's own paths.
const AgentModeFileNameEnv = "RUNS_ON_SHOW_COSTS_FILE_NAME"

// AgentModeOff turns the agent's report off. The show_costs values inline and
// summary are passed through as they are.
const AgentModeOff = "off"

// AgentModeFile returns where to ask the agent for a display mode, or "" when
// the agent does not report costs and the action must query the cost API.
func AgentModeFile() string {
	name := strings.TrimSpace(os.Getenv(AgentModeFileNameEnv))
	runnerTemp := os.Getenv("RUNNER_TEMP")
	if name == "" || runnerTemp == "" || filepath.Base(name) != name {
		return ""
	}
	return filepath.Join(runnerTemp, name)
}

// RequestAgentMode tells the agent how to display the job cost.
func RequestAgentMode(path string, mode string) error {
	return os.WriteFile(path, []byte(mode+"\n"), 0o644)
}
