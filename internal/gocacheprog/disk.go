package gocacheprog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// diskStore is the local layer. The go command reads every hit from the
// returned DiskPath, so even remote hits land here first. Outputs live at
// o/<outputID> and action entries at a/<actionID>, whose content is
// "<outputID hex> <size> <unix nanos>".
type diskStore struct {
	dir string
}

func newDiskStore(dir string) (*diskStore, error) {
	for _, sub := range []string{"a", "o", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &diskStore{dir: dir}, nil
}

func (d *diskStore) outputPath(outputID []byte) string {
	return filepath.Join(d.dir, "o", hex.EncodeToString(outputID))
}

func (d *diskStore) get(actionID []byte) (Entry, bool) {
	data, err := os.ReadFile(filepath.Join(d.dir, "a", hex.EncodeToString(actionID)))
	if err != nil {
		return Entry{}, false
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return Entry{}, false
	}
	outputID, err := hex.DecodeString(fields[0])
	if err != nil {
		return Entry{}, false
	}
	size, err1 := strconv.ParseInt(fields[1], 10, 64)
	nanos, err2 := strconv.ParseInt(fields[2], 10, 64)
	if err1 != nil || err2 != nil {
		return Entry{}, false
	}
	path := d.outputPath(outputID)
	info, err := os.Stat(path)
	if err != nil || info.Size() != size {
		return Entry{}, false
	}
	return Entry{OutputID: outputID, Size: size, Time: time.Unix(0, nanos), DiskPath: path}, true
}

func (d *diskStore) put(actionID, outputID, body []byte, now time.Time) (string, error) {
	path := d.outputPath(outputID)
	if info, err := os.Stat(path); err != nil || info.Size() != int64(len(body)) {
		if _, err := d.writeAtomically(path, bytes.NewReader(body), nil); err != nil {
			return "", err
		}
	}
	return path, d.putAction(actionID, outputID, int64(len(body)), now)
}

// putStream stores an output read from body without holding it in memory. The
// go command's output ID is the SHA-256 of the output: one that does not
// match is not stored.
func (d *diskStore) putStream(actionID, outputID []byte, body io.Reader, now time.Time) error {
	size, err := d.writeAtomically(d.outputPath(outputID), body, outputID)
	if err != nil {
		return err
	}
	return d.putAction(actionID, outputID, size, now)
}

func (d *diskStore) putAction(actionID, outputID []byte, size int64, now time.Time) error {
	entry := fmt.Sprintf("%x %d %d\n", outputID, size, now.UnixNano())
	_, err := d.writeAtomically(filepath.Join(d.dir, "a", hex.EncodeToString(actionID)), strings.NewReader(entry), nil)
	return err
}

// writeAtomically writes data to path and returns its size. With a non-nil
// sum, data must hash to it.
func (d *diskStore) writeAtomically(path string, data io.Reader, sum []byte) (int64, error) {
	f, err := os.CreateTemp(filepath.Join(d.dir, "tmp"), "put-*")
	if err != nil {
		return 0, err
	}
	tmp := f.Name()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hash), data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil && sum != nil && !bytes.Equal(hash.Sum(nil), sum) {
		err = errCorruptEntry
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		// Windows refuses to replace a file that another go command's helper
		// has open. What is there is then a complete earlier write: the same
		// output, or an entry for the same action.
		if _, statErr := os.Stat(path); statErr != nil {
			return 0, err
		}
	}
	return size, nil
}
