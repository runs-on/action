package mbx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMbxBuildsThroughTheHelper drives the real mbx command against the
// helper and an in-memory cache service: a push to a protected branch builds
// and publishes, then a pull request with an empty local store builds from
// what it published. It needs mbx and Cargo on PATH.
func TestMbxBuildsThroughTheHelper(t *testing.T) {
	mbxBin, err := exec.LookPath("mbx")
	if err != nil {
		t.Skip("mbx not found")
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not found")
	}
	srv, cache, server := newTestServer(t)
	// mbx's agent can outlive the build for a moment, still writing to its
	// store, so the directory is removed on a best-effort basis.
	dir, err := os.MkdirTemp("", "mbx-contract-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	workspace := filepath.Join(dir, "workspace")
	files := map[string]string{
		"Cargo.toml":          "[workspace]\nmembers = [\"lib\", \"app\"]\nresolver = \"2\"\n",
		"lib/Cargo.toml":      "[package]\nname = \"mbxcheck-lib\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"lib/src/lib.rs":      "pub fn greeting() -> &'static str {\n    \"served from Magic Cache\"\n}\n",
		"app/Cargo.toml":      "[package]\nname = \"mbxcheck-app\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nmbxcheck-lib = { path = \"../lib\" }\n",
		"app/src/main.rs":     "fn main() {\n    println!(\"{}\", mbxcheck_lib::greeting());\n}\n",
		"app/build.rs":        "fn main() {\n    println!(\"cargo::rerun-if-changed=build.rs\");\n}\n",
		"lib/src/nothing.txt": "",
	}
	for name, content := range files {
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// mbx keys its predictions by the lockfile, which a project commits.
	lockfile := exec.Command("cargo", "generate-lockfile")
	lockfile.Dir = workspace
	if out, err := lockfile.CombinedOutput(); err != nil {
		t.Fatalf("cargo generate-lockfile: %v\n%s", err, out)
	}
	// Both builds use the same paths, as two jobs on fresh runners do: mbx
	// keys its predictions by them. The second starts from an empty store
	// and target directory.
	store := filepath.Join(dir, "store")
	build := func(event string) string {
		t.Helper()
		cmd := exec.Command(mbxBin, "build", "--workspace")
		cmd.Dir = workspace
		cmd.Env = append(mbxFreeEnviron(),
			"MBX_REMOTE_URL="+srv.URL+"/",
			"MBX_REMOTE_TOKEN="+testClientToken,
			"MBX_REMOTE_NAMESPACE=contract",
			"MBX_CACHE_DIR="+store,
			"MBX_GC_AUTO=0",
			"CARGO_TARGET_DIR="+filepath.Join(dir, "target"),
			"XDG_CONFIG_HOME="+filepath.Join(dir, "config"),
			"CI=true",
			"GITHUB_ACTIONS=true",
			"GITHUB_EVENT_NAME="+event,
			"GITHUB_REF_TYPE=branch",
			"GITHUB_REF_PROTECTED=true",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mbx build (%s): %v\n%s", event, err, out)
		}
		return string(out)
	}

	out := build("push")
	pushed := server.Stats()
	if pushed.Uploads == 0 || cache.entryCount() == 0 {
		t.Fatalf("the push published nothing: %+v\n%s", pushed, out)
	}
	t.Logf("push: %+v\n%s", pushed, out)

	for _, path := range []string{store, filepath.Join(dir, "target")} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	out = build("pull_request")
	pulled := server.Stats()
	t.Logf("pull request: %+v\n%s", pulled, out)
	if pulled.Hits <= pushed.Hits || cache.downloadCount() == 0 {
		t.Fatalf("the pull request did not build from the remote cache: %+v\n%s", pulled, out)
	}
	if pulled.Uploads != pushed.Uploads || pulled.Manifests != pushed.Manifests {
		t.Errorf("the pull request published: %+v, after the push %+v", pulled, pushed)
	}
	if pulled.Errors != 0 || pulled.Refused != 0 {
		t.Errorf("errors: %+v", pulled)
	}
}

// mbxFreeEnviron is the test's environment without the settings that would
// point mbx or Cargo somewhere else.
func mbxFreeEnviron() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "MBX_") || strings.HasPrefix(name, "GITHUB_") || strings.HasPrefix(name, "XDG_") ||
			name == "CI" || name == "CARGO_TARGET_DIR" || name == "RUSTC_WRAPPER" || name == "CARGO_BUILD_RUSTC_WRAPPER" {
			continue
		}
		env = append(env, kv)
	}
	return env
}
