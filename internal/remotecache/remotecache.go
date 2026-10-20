// Package remotecache reads the remote_cache input, which lists the build
// tools whose remote caches the RunsOn agent stores. Like sticky_cache, it
// takes one record per line: a tool, then comma-separated key=value options.
package remotecache

import (
	"fmt"
	"slices"
	"strings"
)

// Tools are the tools remote_cache configures.
var Tools = []string{"bazel", "go", "mbx", "sccache"}

// Request is one tool to configure.
type Request struct {
	Tool string
	// Tests is go's tests=true: test results are shared between jobs too.
	Tests bool
}

// Parse returns the input's requests, in order. A record it cannot read is
// skipped, with a problem saying why: the other tools are still configured,
// as when one of them cannot be.
func Parse(input string) ([]Request, []string) {
	var requests []Request
	var problems []string
	for i, line := range strings.Split(input, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		request, err := parseRecord(line)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("line %d: %v", i+1, err))
		case slices.ContainsFunc(requests, func(r Request) bool { return r.Tool == request.Tool }):
			problems = append(problems, fmt.Sprintf("line %d: %s is listed twice; the first one is used", i+1, request.Tool))
		default:
			requests = append(requests, request)
		}
	}
	return requests, problems
}

func parseRecord(line string) (Request, error) {
	fields := strings.Split(line, ",")
	request := Request{Tool: strings.ToLower(strings.TrimSpace(fields[0]))}
	if !slices.Contains(Tools, request.Tool) {
		return Request{}, fmt.Errorf("unknown tool %q (known: %s)", request.Tool, strings.Join(Tools, ", "))
	}
	for _, field := range fields[1:] {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.ToLower(strings.TrimSpace(value))
		switch {
		case !ok || key == "" || value == "":
			return Request{}, fmt.Errorf("%s option %q is not key=value; list one tool per line", request.Tool, strings.TrimSpace(field))
		case request.Tool == "go" && key == "tests" && (value == "true" || value == "false"):
			request.Tests = value == "true"
		case request.Tool == "go" && key == "tests":
			return Request{}, fmt.Errorf("go option tests is %q, want true or false", value)
		default:
			return Request{}, fmt.Errorf("%s has no option %q", request.Tool, key)
		}
	}
	return request, nil
}
