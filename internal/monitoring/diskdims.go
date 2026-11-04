package monitoring

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

const (
	procMountInfo = "/proc/self/mountinfo"
	sysBlock      = "/sys/block"
	sysDevBlock   = "/sys/dev/block"
)

// diskResources are the mount points the CloudWatch agent reports disk metrics for.
var diskResources = []string{"/", "/tmp", "/var/lib/docker", "/home/runner"}

type mountEntry struct {
	majorMinor string
	fstype     string
	source     string
}

// diskMetricDimensions returns the dimensions, besides InstanceId, under which
// the CloudWatch agent publishes the disk metrics of mountPoint, and false when
// nothing is mounted there (the agent then publishes nothing for it).
//
// It mirrors the agent: device is the mount source without /dev/, with
// /dev/root resolved to the real device, and VolumeId is the serial of the
// disk behind that device. Each mount therefore gets its own volume, e.g. a
// sticky disk bind-mounted at /var/lib/docker.
func diskMetricDimensions(mountInfo, sysBlockDir, sysDevBlockDir, mountPoint string) ([]types.Dimension, bool) {
	mount, ok := findMount(mountInfo, mountPoint)
	if !ok {
		return nil, false
	}
	device := mount.source
	if device == "/dev/root" {
		if target, err := os.Readlink(filepath.Join(sysDevBlockDir, mount.majorMinor)); err == nil {
			device = "/dev/" + filepath.Base(target)
		}
	}
	device = strings.ReplaceAll(device, "/dev/", "")

	dimensions := []types.Dimension{
		{Name: aws.String("device"), Value: aws.String(device)},
		{Name: aws.String("fstype"), Value: aws.String(mount.fstype)},
		{Name: aws.String("path"), Value: aws.String(mountPoint)},
	}
	if volumeID := volumeIDForDevice(sysBlockDir, device); volumeID != "" {
		dimensions = append(dimensions, types.Dimension{Name: aws.String("VolumeId"), Value: aws.String(volumeID)})
	}
	return dimensions, true
}

// findMount returns the last entry mounted at mountPoint, which is the one
// visible at that path.
func findMount(mountInfo, mountPoint string) (mountEntry, bool) {
	file, err := os.Open(mountInfo)
	if err != nil {
		return mountEntry{}, false
	}
	defer file.Close()

	var found mountEntry
	ok := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// id parent major:minor root mount-point options [optional...] - fstype source super-options
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[4] != mountPoint {
			continue
		}
		for i := 6; i+2 < len(fields); i++ {
			if fields[i] == "-" {
				found = mountEntry{majorMinor: fields[2], fstype: fields[i+1], source: fields[i+2]}
				ok = true
				break
			}
		}
	}
	return found, ok
}

// volumeIDForDevice mirrors the agent's lookup: each disk under /sys/block
// maps to its serial, and a partition matches its disk by name prefix. EBS
// serials read vol0123..., which the agent publishes as vol-0123....
func volumeIDForDevice(sysBlockDir, device string) string {
	entries, err := os.ReadDir(sysBlockDir)
	if err != nil {
		return ""
	}
	match, volumeID := "", ""
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "loop") || !strings.HasPrefix(device, name) || len(name) <= len(match) {
			continue
		}
		serial, err := os.ReadFile(filepath.Join(sysBlockDir, name, "device", "serial"))
		if err != nil {
			continue
		}
		if serial := strings.TrimSpace(string(serial)); serial != "" {
			match, volumeID = name, formatVolumeSerial(serial)
		}
	}
	return volumeID
}

func formatVolumeSerial(serial string) string {
	suffix, ok := strings.CutPrefix(serial, "vol")
	if !ok || strings.HasPrefix(suffix, "-") {
		return serial
	}
	return "vol-" + suffix
}
