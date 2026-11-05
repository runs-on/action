package gocacheprog

import (
	"bytes"
	"crypto/sha256"
	"io"
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
