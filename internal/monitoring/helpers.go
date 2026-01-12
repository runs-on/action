package monitoring

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const DEFAULT_NETWORK_INTERFACE = "enp39s0"
const DEFAULT_DISK_DEVICE = "nvme0n1p1"

// detectPrimaryNetworkInterface finds the primary network interface (excluding loopback and docker)
func detectPrimaryNetworkInterface() string {
	// Try to get the interface used for the default route
	cmd := exec.Command("ip", "route", "show", "default")
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.Contains(line, "dev ") {
				parts := strings.Fields(line)
				for i, part := range parts {
					if part == "dev" && i+1 < len(parts) {
						iface := parts[i+1]
						// Skip docker and loopback interfaces
						if !strings.HasPrefix(iface, "docker") && !strings.HasPrefix(iface, "br-") && iface != "lo" {
							return iface
						}
					}
				}
			}
		}
	}

	// Fallback: list network interfaces and pick the first non-loopback, non-docker one
	cmd = exec.Command("ls", "/sys/class/net")
	output, err = cmd.Output()
	if err != nil {
		return DEFAULT_NETWORK_INTERFACE // ultimate fallback
	}

	interfaces := strings.Fields(string(output))
	for _, iface := range interfaces {
		if iface != "lo" && !strings.HasPrefix(iface, "docker") && !strings.HasPrefix(iface, "br-") {
			return iface
		}
	}

	return DEFAULT_NETWORK_INTERFACE // ultimate fallback
}

// detectRootDiskDevice finds the disk device that contains the root filesystem
func detectRootDiskDevice() string {
	// Read /proc/mounts to find what device / is mounted on
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return DEFAULT_DISK_DEVICE // fallback
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "/" {
			device := fields[0]
			// Extract just the device name from /dev/xxx
			if strings.HasPrefix(device, "/dev/") {
				deviceName := strings.TrimPrefix(device, "/dev/")
				return deviceName
			}
		}
	}

	// Alternative: try to get the device from df command
	cmd := exec.Command("df", "/")
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		if len(lines) > 1 {
			fields := strings.Fields(lines[1])
			if len(fields) > 0 {
				device := fields[0]
				if strings.HasPrefix(device, "/dev/") {
					deviceName := strings.TrimPrefix(device, "/dev/")
					return deviceName
				}
			}
		}
	}

	return DEFAULT_DISK_DEVICE // ultimate fallback
}

// getNetworkInterface returns the network interface to use based on config
func getNetworkInterface(networkInterface string) string {
	if networkInterface == "auto" {
		return detectPrimaryNetworkInterface()
	}
	return networkInterface
}

// getDiskDevice returns the disk device to use based on config
func getDiskDevice(diskDevice string) string {
	if diskDevice == "auto" {
		return detectRootDiskDevice()
	}
	return diskDevice
}

// getVolumeID extracts the EBS volume ID from a disk device name
// It reads the MODEL and SERIAL from /sys/block/${BASE}/device/ and converts
// the serial to the volume ID format (vol-...)
func getVolumeID(diskDevice string) (string, error) {
	// Extract base device name (remove partition suffix like p1, p2, etc.)
	base := diskDevice
	// Remove partition suffix pattern: p1, p2, etc. (e.g., nvme0n1p1 -> nvme0n1)
	re := regexp.MustCompile(`p\d+$`)
	base = re.ReplaceAllString(base, "")

	// Read MODEL
	modelPath := fmt.Sprintf("/sys/block/%s/device/model", base)
	modelBytes, err := os.ReadFile(modelPath)
	if err != nil {
		return "", fmt.Errorf("failed to read model: %w", err)
	}
	model := strings.TrimSpace(string(modelBytes))

	// Validate MODEL contains "Amazon Elastic Block Store"
	if !strings.Contains(strings.ToLower(model), "amazon elastic block store") {
		return "", fmt.Errorf("%s does not look like an EBS NVMe device (model=%s)", base, model)
	}

	// Read SERIAL
	serialPath := fmt.Sprintf("/sys/block/%s/device/serial", base)
	serialBytes, err := os.ReadFile(serialPath)
	if err != nil {
		return "", fmt.Errorf("failed to read serial: %w", err)
	}
	serial := strings.TrimSpace(string(serialBytes))
	// Remove all whitespace
	serial = strings.ReplaceAll(serial, " ", "")
	serial = strings.ReplaceAll(serial, "\t", "")
	serial = strings.ReplaceAll(serial, "\n", "")

	// Convert SERIAL to VOLUME_ID format
	// Pattern: vol00bb... -> vol-00bb...
	volPattern := regexp.MustCompile(`^vol[0-9a-f]+$`)
	volWithDashPattern := regexp.MustCompile(`^vol-[0-9a-f]+$`)

	var volumeID string
	if volPattern.MatchString(serial) {
		// vol00bb... -> vol-00bb...
		volumeID = "vol-" + serial[3:]
	} else if volWithDashPattern.MatchString(serial) {
		// Already in correct format
		volumeID = serial
	} else {
		return "", fmt.Errorf("unexpected NVMe serial format: %s", serial)
	}

	return volumeID, nil
}

// calculateMin returns the minimum value in a slice
func calculateMin(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	min := data[0]
	for _, v := range data {
		if v < min {
			min = v
		}
	}
	return min
}

// calculateMax returns the maximum value in a slice
func calculateMax(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	max := data[0]
	for _, v := range data {
		if v > max {
			max = v
		}
	}
	return max
}

// calculateStats computes min, max, and average of a slice of floats
func calculateStats(data []float64) (min, max, avg float64) {
	if len(data) == 0 {
		return 0, 0, 0
	}

	min, max = data[0], data[0]
	sum := 0.0

	for _, v := range data {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}

	avg = sum / float64(len(data))
	return
}
