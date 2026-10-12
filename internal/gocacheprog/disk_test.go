package gocacheprog

import (
	"bytes"
	"crypto/sha256"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestPutStreamStoresOnlyOutputsThatMatchTheirID(t *testing.T) {
	disk, err := newDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	output := "compiled package"
	sum := sha256.Sum256([]byte(output))
	good, bad := bytes.Repeat([]byte{1}, actionIDSize), bytes.Repeat([]byte{2}, actionIDSize)

	if err := disk.putStream(bad, sum[:], strings.NewReader("something else"), time.Now()); err == nil {
		t.Error("stored an output that does not hash to its ID")
	}
	if _, _, ok := disk.get(bad); ok {
		t.Error("an output that does not hash to its ID is served")
	}

	if err := disk.putStream(good, sum[:], strings.NewReader(output), time.Now()); err != nil {
		t.Fatal(err)
	}
	if entry, _, ok := disk.get(good); !ok || entry.Size != int64(len(output)) {
		t.Errorf("got %+v, %v, want the stored output", entry, ok)
	}

	// A download that breaks off does not replace what is stored.
	broken := io.MultiReader(strings.NewReader(output[:4]), iotest.ErrReader(io.ErrUnexpectedEOF))
	if err := disk.putStream(good, sum[:], broken, time.Now()); err == nil {
		t.Error("stored an output whose download broke off")
	}
	if entry, _, ok := disk.get(good); !ok || entry.Size != int64(len(output)) {
		t.Errorf("got %+v, %v after a broken download, want the stored output", entry, ok)
	}
}

// The go command starts the helper, as it starts this test: its command line
// tells go build from go test, which use different entries.
func TestParentCommandLineIsTheGoCommand(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("the parent's command line is only read on Linux and Windows")
	}
	// Linux separates the arguments with NUL; Windows gives one line, where
	// the program may be quoted.
	line := string(parentCommandLine())
	args := strings.FieldsFunc(line, func(r rune) bool { return r == 0 || r == ' ' })
	if len(args) < 2 || !slices.Contains(args[1:], "test") {
		t.Fatalf("parent command line is %q, want a go test command", line)
	}
	if program := strings.TrimSuffix(strings.Trim(args[0], `"`), ".exe"); filepath.Base(filepath.ToSlash(program)) != "go" {
		t.Errorf("parent program is %q, want the go command", args[0])
	}
}
