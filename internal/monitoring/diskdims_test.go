package monitoring

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestDiskMetricDimensionsResolvePerMountVolume(t *testing.T) {
	root := t.TempDir()
	mountInfo := filepath.Join(root, "mountinfo")
	if err := os.WriteFile(mountInfo, []byte(`22 1 259:1 / / rw,relatime shared:1 - ext4 /dev/root rw,discard
30 22 0:27 / /tmp rw,nosuid,nodev shared:5 - tmpfs tmpfs rw
40 22 259:3 /mounts/docker /var/lib/docker rw,relatime shared:9 - ext4 /dev/nvme1n1 rw
`), 0o644); err != nil {
		t.Fatal(err)
	}
	sysBlockDir := filepath.Join(root, "sys", "block")
	for disk, serial := range map[string]string{
		"nvme0n1": "vol0123456789abcdef0\n",
		"nvme1n1": "vol0fedcba9876543210   \n",
		"loop0":   "",
	} {
		if err := os.MkdirAll(filepath.Join(sysBlockDir, disk, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
		if serial != "" {
			if err := os.WriteFile(filepath.Join(sysBlockDir, disk, "device", "serial"), []byte(serial), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	sysDevBlockDir := filepath.Join(root, "sys", "dev", "block")
	if err := os.MkdirAll(sysDevBlockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../devices/pci0000:00/nvme/nvme0/nvme0n1/nvme0n1p1", filepath.Join(sysDevBlockDir, "259:1")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		mountPoint string
		want       map[string]string
	}{
		{"/", map[string]string{"device": "nvme0n1p1", "fstype": "ext4", "path": "/", "VolumeId": "vol-0123456789abcdef0"}},
		// A second volume on /var/lib/docker must report its own VolumeId, not the root's.
		{"/var/lib/docker", map[string]string{"device": "nvme1n1", "fstype": "ext4", "path": "/var/lib/docker", "VolumeId": "vol-0fedcba9876543210"}},
		{"/tmp", map[string]string{"device": "tmpfs", "fstype": "tmpfs", "path": "/tmp"}},
		{"/home/runner", nil},
	}
	for _, tt := range tests {
		dimensions, mounted := diskMetricDimensions(mountInfo, sysBlockDir, sysDevBlockDir, tt.mountPoint)
		if mounted != (tt.want != nil) {
			t.Fatalf("%s: mounted = %v, want %v", tt.mountPoint, mounted, tt.want != nil)
		}
		if !mounted {
			continue
		}
		got := map[string]string{}
		for _, dimension := range dimensions {
			got[aws.ToString(dimension.Name)] = aws.ToString(dimension.Value)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: dimensions = %v, want %v", tt.mountPoint, got, tt.want)
		}
	}
}
