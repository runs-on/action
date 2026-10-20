package remotecache

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name         string
		input        string
		want         []Request
		wantProblems []string
	}{
		{name: "empty"},
		{name: "one tool", input: "bazel", want: []Request{{Tool: "bazel"}}},
		{
			name:  "one per line, with options and blank lines",
			input: "bazel\n\n  go, tests=true \nSccache\n",
			want:  []Request{{Tool: "bazel"}, {Tool: "go", Tests: true}, {Tool: "sccache"}},
		},
		{name: "go without test results", input: "go,tests=false", want: []Request{{Tool: "go"}}},
		{
			name:         "a bad record leaves the others",
			input:        "bazel\nccache\nmbx",
			want:         []Request{{Tool: "bazel"}, {Tool: "mbx"}},
			wantProblems: []string{`line 2: unknown tool "ccache"`},
		},
		{name: "tools on one line", input: "bazel,go", wantProblems: []string{"list one tool per line"}},
		{name: "option the tool lacks", input: "bazel,tests=true", wantProblems: []string{`bazel has no option "tests"`}},
		{name: "bad option value", input: "go,tests=yes", wantProblems: []string{`tests is "yes"`}},
		{
			name:         "listed twice",
			input:        "go,tests=true\ngo",
			want:         []Request{{Tool: "go", Tests: true}},
			wantProblems: []string{"line 2: go is listed twice"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, problems := Parse(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("requests = %+v, want %+v", got, tc.want)
			}
			if len(problems) != len(tc.wantProblems) {
				t.Fatalf("problems = %q, want %d", problems, len(tc.wantProblems))
			}
			for i, want := range tc.wantProblems {
				if !strings.Contains(problems[i], want) {
					t.Errorf("problem %q, want it to contain %q", problems[i], want)
				}
			}
		})
	}
}
