package monitoring

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetEBSVolumeID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		device      string
		serial      string
		want        string
		wantErr     bool
		writeSerial bool
	}{
		{name: "partitioned current ID", device: "nvme0n1p1", serial: "vol0123456789abcdef0\n", want: "vol-0123456789abcdef0", writeSerial: true},
		{name: "unpartitioned current ID", device: "nvme0n1", serial: "vol-0123456789abcdef0", want: "vol-0123456789abcdef0", writeSerial: true},
		{name: "old ID with whitespace", device: "nvme0n1p12", serial: " vol 01234567 \n", want: "vol-01234567", writeSerial: true},
		{name: "invalid serial", device: "nvme0n1p1", serial: "AWS42", wantErr: true, writeSerial: true},
		{name: "missing serial", device: "nvme0n1p1", wantErr: true},
		{name: "unsafe device", device: "../nvme0n1p1", wantErr: true},
		{name: "unsupported device", device: "xvda1", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sysBlockRoot := t.TempDir()
			if tt.writeSerial {
				serialDir := filepath.Join(sysBlockRoot, "nvme0n1", "device")
				if err := os.MkdirAll(serialDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(serialDir, "serial"), []byte(tt.serial), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := getEBSVolumeID(sysBlockRoot, tt.device)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got volume ID %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("getEBSVolumeID() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("getEBSVolumeID() = %q, want %q", got, tt.want)
			}
		})
	}
}
