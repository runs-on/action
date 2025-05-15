package snapshot

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/rs/zerolog"
)

const (
	// Environment variable to get the current git branch
	gitBranchEnvVar = "GITHUB_REF_NAME" // Or appropriate env var for your CI

	// Tags used for resource identification
	snapshotBranchTagKey = "runs-on-snapshot-branch"
	latestSnapshotTagKey = "runs-on-latest-snapshot-for-branch"
	jobVolumeTagKey      = "runs-on-job-volume"
	nameTagKey           = "Name"
	timestampTagKey      = "runs-on-timestamp"

	// Default Volume Specifications
	defaultVolumeSizeGiB        int32 = 60
	defaultVolumeType                 = types.VolumeTypeGp3
	defaultVolumeIops           int32 = 3000
	defaultVolumeThroughputMBps int32 = 700        // As per user's README
	suggestedDeviceName               = "/dev/sdf" // AWS might assign /dev/xvdf etc.
)

// Snapshot struct from the original file - kept for reference, but not directly used by new funcs
type Snapshot struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Snapshotter interface from the original file - kept for reference
type Snapshotter interface {
	CreateSnapshot(ctx context.Context, snapshot *Snapshot) error
	GetSnapshot(ctx context.Context, id string) (*Snapshot, error)
	DeleteSnapshot(ctx context.Context, id string) error
}

// AWSSnapshotter provides methods to manage EBS snapshots and volumes.
type AWSSnapshotter struct {
	logger    *zerolog.Logger
	config    SnapshotterConfig
	ec2Client *ec2.Client
}

type SnapshotterConfig struct {
	GithubRef  string
	InstanceID string
	Az         string
}

// NewAWSSnapshotter creates a new AWSSnapshotter instance.
// It initializes the AWS SDK configuration and fetches EC2 instance metadata.
func NewAWSSnapshotter(ctx context.Context, logger *zerolog.Logger, cfg SnapshotterConfig) (*AWSSnapshotter, error) {
	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS SDK config: %w", err)
	}

	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("instanceID is required")
	}

	if cfg.Az == "" {
		return nil, fmt.Errorf("az is required")
	}

	if cfg.GithubRef == "" {
		return nil, fmt.Errorf("githubRef is required")
	}

	return &AWSSnapshotter{
		logger:    logger,
		config:    cfg,
		ec2Client: ec2.NewFromConfig(awsConfig),
	}, nil
}

// RestoreSnapshotOutput holds the results of RestoreSnapshot.
type RestoreSnapshotOutput struct {
	VolumeID   string
	DeviceName string
}

// RestoreSnapshot finds the latest snapshot for the current git branch,
// creates a volume from it (or a new volume if no snapshot exists),
// attaches it to the instance, and mounts it to the specified mountPoint.
func (s *AWSSnapshotter) RestoreSnapshot(ctx context.Context, mountPoint string) (*RestoreSnapshotOutput, error) {
	gitBranch := s.config.GithubRef
	s.logger.Info().Msgf("RestoreSnapshot: Using git ref: %s", gitBranch)

	var newVolume *types.Volume
	var volumeIsNewAndUnformatted bool
	currentTime := time.Now()
	jobIdentifier := currentTime.Format("20060102-150405")

	// 1. Find latest snapshot for branch
	s.logger.Info().Msgf("RestoreSnapshot: Searching for the latest snapshot for branch: %s", gitBranch)
	snapshotsOutput, err := s.ec2Client.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		Filters: []types.Filter{
			{Name: aws.String("tag:" + snapshotBranchTagKey), Values: []string{gitBranch}},
			{Name: aws.String("tag:" + latestSnapshotTagKey), Values: []string{"true"}},
			{Name: aws.String("status"), Values: []string{string(types.SnapshotStateCompleted)}},
		},
		OwnerIds: []string{"self"}, // Or specific account ID if needed
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe snapshots for branch %s: %w", gitBranch, err)
	}

	var latestSnapshot *types.Snapshot
	if len(snapshotsOutput.Snapshots) > 0 {
		// Assuming only one snapshot should have latestSnapshotTagKey=true for a branch
		latestSnapshot = &snapshotsOutput.Snapshots[0]
		s.logger.Info().Msgf("RestoreSnapshot: Found latest snapshot %s for branch %s", *latestSnapshot.SnapshotId, gitBranch)
	} else {
		s.logger.Info().Msgf("RestoreSnapshot: No existing snapshot found for branch %s. A new volume will be created.", gitBranch)
	}

	commonVolumeTags := []types.Tag{
		{Key: aws.String(jobVolumeTagKey), Value: aws.String(jobIdentifier)},
		{Key: aws.String(snapshotBranchTagKey), Value: aws.String(gitBranch)}, // Tag volume with branch for easier manual lookup
		{Key: aws.String(nameTagKey), Value: aws.String(fmt.Sprintf("job-volume-%s-%s", gitBranch, jobIdentifier))},
		{Key: aws.String(timestampTagKey), Value: aws.String(currentTime.Format(time.RFC3339))},
	}

	if latestSnapshot != nil {
		// 2. Create Volume from Snapshot
		s.logger.Info().Msgf("RestoreSnapshot: Creating volume from snapshot %s", *latestSnapshot.SnapshotId)
		createVolumeOutput, err := s.ec2Client.CreateVolume(ctx, &ec2.CreateVolumeInput{
			SnapshotId:       latestSnapshot.SnapshotId,
			AvailabilityZone: latestSnapshot.AvailabilityZone,
			VolumeType:       defaultVolumeType,
			// Size is determined by snapshot, but can be increased. Ensure it meets min throughput if increasing.
			// Size:             aws.Int32(defaultVolumeSizeGiB),
			Iops:       aws.Int32(defaultVolumeIops),
			Throughput: aws.Int32(defaultVolumeThroughputMBps),
			TagSpecifications: []types.TagSpecification{
				{ResourceType: types.ResourceTypeVolume, Tags: commonVolumeTags},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create volume from snapshot %s: %w", *latestSnapshot.SnapshotId, err)
		}
		newVolume = &types.Volume{VolumeId: createVolumeOutput.VolumeId}
		volumeIsNewAndUnformatted = false // Volume from snapshot is already formatted
		s.logger.Info().Msgf("RestoreSnapshot: Created volume %s from snapshot %s", *newVolume.VolumeId, *latestSnapshot.SnapshotId)
	} else {
		// 3. No snapshot found, create a new volume
		s.logger.Info().Msgf("RestoreSnapshot: Creating a new blank volume")
		createVolumeOutput, err := s.ec2Client.CreateVolume(ctx, &ec2.CreateVolumeInput{
			AvailabilityZone: aws.String(s.config.Az),
			VolumeType:       defaultVolumeType,
			Size:             aws.Int32(defaultVolumeSizeGiB),
			Iops:             aws.Int32(defaultVolumeIops),
			Throughput:       aws.Int32(defaultVolumeThroughputMBps),
			TagSpecifications: []types.TagSpecification{
				{ResourceType: types.ResourceTypeVolume, Tags: commonVolumeTags},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create new volume: %w", err)
		}
		newVolume = &types.Volume{VolumeId: createVolumeOutput.VolumeId}
		volumeIsNewAndUnformatted = true // New volume needs formatting
		s.logger.Info().Msgf("RestoreSnapshot: Created new blank volume %s", *newVolume.VolumeId)
	}

	defer func() {
		if err != nil {
			s.logger.Error().Msgf("RestoreSnapshot: Error: %v", err)
			if newVolume != nil {
				s.logger.Info().Msgf("RestoreSnapshot: Deleting volume %s", *newVolume.VolumeId)
				_, err := s.ec2Client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: newVolume.VolumeId})
				if err != nil {
					s.logger.Error().Msgf("RestoreSnapshot: Error deleting volume %s: %v", *newVolume.VolumeId, err)
				}
			}
		}
	}()

	// 4. Wait for volume to be 'available'
	s.logger.Info().Msgf("RestoreSnapshot: Waiting for volume %s to become available...", *newVolume.VolumeId)
	volumeAvailableWaiter := ec2.NewVolumeAvailableWaiter(s.ec2Client)
	if err := volumeAvailableWaiter.Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{*newVolume.VolumeId}}, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("volume %s did not become available in time: %w", *newVolume.VolumeId, err)
	}
	s.logger.Info().Msgf("RestoreSnapshot: Volume %s is available.", *newVolume.VolumeId)

	// 5. Attach Volume
	s.logger.Info().Msgf("RestoreSnapshot: Attaching volume %s to instance %s as %s", *newVolume.VolumeId, s.config.InstanceID, suggestedDeviceName)
	attachOutput, err := s.ec2Client.AttachVolume(ctx, &ec2.AttachVolumeInput{
		Device:     aws.String(suggestedDeviceName),
		InstanceId: aws.String(s.config.InstanceID),
		VolumeId:   newVolume.VolumeId,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to attach volume %s to instance %s: %w", *newVolume.VolumeId, s.config.InstanceID, err)
	}
	actualDeviceName := *attachOutput.Device
	s.logger.Info().Msgf("RestoreSnapshot: Volume %s attach initiated, device hint: %s. Waiting for attachment...", *newVolume.VolumeId, actualDeviceName)

	volumeInUseWaiter := ec2.NewVolumeInUseWaiter(s.ec2Client)
	if err := volumeInUseWaiter.Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{*newVolume.VolumeId}}, 2*time.Minute); err != nil {
		// Check actual attachment state if waiter fails
		descVol, descErr := s.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{*newVolume.VolumeId}})
		if descErr == nil && len(descVol.Volumes) > 0 && len(descVol.Volumes[0].Attachments) > 0 {
			att := descVol.Volumes[0].Attachments[0]
			if att.State == types.VolumeAttachmentStateAttached {
				actualDeviceName = *att.Device // Ensure we have the correct device name
				s.logger.Info().Msgf("RestoreSnapshot: Volume %s successfully attached as %s (waiter timed out but status confirmed).", *newVolume.VolumeId, actualDeviceName)
			} else {
				return nil, fmt.Errorf("volume %s did not attach successfully (state: %s): %w", *newVolume.VolumeId, att.State, err)
			}
		} else {
			return nil, fmt.Errorf("volume %s did not attach successfully and current state unknown: %w", *newVolume.VolumeId, err)
		}
	} else {
		s.logger.Error().Msgf("RestoreSnapshot: Volume %s did not attach successfully and current state unknown: %v", *newVolume.VolumeId, err)

		// Fetch volume details again to confirm device name, as the attachOutput.Device might be a suggestion
		// and the waiter confirms attachment, not necessarily the final device name if it changed.
		descVolOutput, descErr := s.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{*newVolume.VolumeId}})
		if descErr == nil && len(descVolOutput.Volumes) > 0 && len(descVolOutput.Volumes[0].Attachments) > 0 {
			actualDeviceName = *descVolOutput.Volumes[0].Attachments[0].Device
		} // else stick with actualDeviceName from attachOutput or waiter confirmation
		s.logger.Info().Msgf("RestoreSnapshot: Volume %s attached as %s.", *newVolume.VolumeId, actualDeviceName)
	}

	// 6. Mounting & Docker
	s.logger.Info().Msgf("RestoreSnapshot: Stopping docker service...")
	if _, err := s.runCommand(ctx, "sudo", "systemctl", "stop", "docker"); err != nil {
		s.logger.Warn().Msgf("RestoreSnapshot: failed to stop docker (may not be running or installed): %v", err)
	}

	s.logger.Info().Msgf("RestoreSnapshot: Attempting to unmount %s (defensive)", mountPoint)
	if _, err := s.runCommand(ctx, "sudo", "umount", mountPoint); err != nil {
		s.logger.Warn().Msgf("RestoreSnapshot: Defensive unmount of %s failed (likely not mounted): %v", mountPoint, err)
	}

	// display disk cofniguration
	s.logger.Info().Msgf("RestoreSnapshot: Displaying disk configuration...")

	// actual device name is the last entry from `lsblk -d -n -o PATH,MODEL` that has a MODEL = 'Amazon Elastic Block Store'
	lsblkOutput, err := s.runCommand(ctx, "lsblk", "-d", "-n", "-o", "PATH,MODEL")
	if err != nil {
		s.logger.Warn().Msgf("RestoreSnapshot: Failed to display disk configuration: %v", err)
	}
	for _, line := range strings.Split(string(lsblkOutput), "\n") {
		fields := strings.Fields(line)
		// first volume is the root volume, so we need to skip it
		if len(fields) > 1 && fields[1] == "Amazon Elastic Block Store" {
			actualDeviceName = fields[0]
		}
	}
	s.logger.Info().Msgf("RestoreSnapshot: Actual device name: %s", actualDeviceName)

	if volumeIsNewAndUnformatted {
		s.logger.Info().Msgf("RestoreSnapshot: Formatting new volume %s (%s) with ext4...", *newVolume.VolumeId, actualDeviceName)
		if _, err := s.runCommand(ctx, "sudo", "mkfs.ext4", "-F", actualDeviceName); err != nil { // -F to force if already formatted by mistake or small
			return nil, fmt.Errorf("failed to format device %s: %w", actualDeviceName, err)
		}
		s.logger.Info().Msgf("RestoreSnapshot: Device %s formatted.", actualDeviceName)
	}

	s.logger.Info().Msgf("RestoreSnapshot: Creating mount point %s if it doesn't exist...", mountPoint)
	if _, err := s.runCommand(ctx, "sudo", "mkdir", "-p", mountPoint); err != nil {
		return nil, fmt.Errorf("failed to create mount point %s: %w", mountPoint, err)
	}

	s.logger.Info().Msgf("RestoreSnapshot: Mounting %s to %s...", actualDeviceName, mountPoint)
	if _, err := s.runCommand(ctx, "sudo", "mount", actualDeviceName, mountPoint); err != nil {
		return nil, fmt.Errorf("failed to mount %s to %s: %w", actualDeviceName, mountPoint, err)
	}
	s.logger.Info().Msgf("RestoreSnapshot: Device %s mounted to %s.", actualDeviceName, mountPoint)

	s.logger.Info().Msgf("RestoreSnapshot: Starting docker service...")
	if _, err := s.runCommand(ctx, "sudo", "systemctl", "start", "docker"); err != nil {
		return nil, fmt.Errorf("failed to start docker after mounting: %w", err)
	}
	s.logger.Info().Msgf("RestoreSnapshot: Docker service started.")

	return &RestoreSnapshotOutput{VolumeID: *newVolume.VolumeId, DeviceName: actualDeviceName}, nil
}

// CreateSnapshotOutput holds the results of CreateSnapshot.
type CreateSnapshotOutput struct {
	SnapshotID string
}

// CreateSnapshot finds the volume associated with the mountPoint, detaches it,
// creates a new snapshot, tags it as the latest for the branch, and cleans up old resources.
func (s *AWSSnapshotter) CreateSnapshot(ctx context.Context, mountPoint string) (*CreateSnapshotOutput, error) {
	gitBranch := s.config.GithubRef
	if gitBranch == "" {
		return nil, fmt.Errorf("git branch environment variable '%s' is not set", gitBranchEnvVar)
	}
	s.logger.Info().Msgf("CreateSnapshot: Using git ref: %s, Instance ID: %s, MountPoint: %s", gitBranch, s.config.InstanceID, mountPoint)

	// 1. Find device for mountPoint
	s.logger.Info().Msgf("CreateSnapshot: Finding device for mount point %s", mountPoint)
	devicePathOutput, err := s.runCommand(ctx, "sudo", "findmnt", "-n", "-o", "SOURCE", "--target", mountPoint)
	if err != nil {
		return nil, fmt.Errorf("failed to find device for mount point %s: %w. Output: %s", mountPoint, err, string(devicePathOutput))
	}
	devicePath := strings.TrimSpace(string(devicePathOutput))
	if devicePath == "" {
		return nil, fmt.Errorf("no device found for mount point %s", mountPoint)
	}
	// Sometimes findmnt returns /dev/root for root, which is not what EC2 knows. We need the actual block device.
	// This is a simplification; a more robust solution might involve checking lsblk or other tools if findmnt isn't sufficient.
	if devicePath == "/dev/root" {
		s.logger.Warn().Msgf("CreateSnapshot: findmnt returned /dev/root, attempting to find actual block device (common on some AMIs)")
		// This is a best-effort; specific AMIs might need different discovery.
		// For now, we assume if it's root, there might be an issue or it's the main EBS, which we shouldn't be snapshotting this way.
		// Consider if this use case implies a different strategy or erroring out.
		// For now, let's try to proceed but log a clear warning. It might work if /dev/root symlinks to the correct /dev/xvda etc.
		s.logger.Warn().Msgf("Warning: Device for %s identified as %s. If this is the root device, snapshotting workflow might need adjustment.", mountPoint, devicePath)
	}
	s.logger.Info().Msgf("CreateSnapshot: Found device %s for mount point %s", devicePath, mountPoint)

	// Find Volume ID for this device on this instance
	s.logger.Info().Msgf("CreateSnapshot: Finding volume ID for device %s on instance %s", devicePath, s.config.InstanceID)
	volumesOutput, err := s.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []types.Filter{
			{Name: aws.String("attachment.instance-id"), Values: []string{s.config.InstanceID}},
			{Name: aws.String("attachment.device"), Values: []string{devicePath}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe volumes for device %s on instance %s: %w", devicePath, s.config.InstanceID, err)
	}
	if len(volumesOutput.Volumes) == 0 {
		return nil, fmt.Errorf("no volume found for device %s attached to instance %s", devicePath, s.config.InstanceID)
	}
	if len(volumesOutput.Volumes) > 1 {
		s.logger.Warn().Msgf("Warning: Found multiple volumes for device %s on instance %s. Using the first one: %s", devicePath, s.config.InstanceID, *volumesOutput.Volumes[0].VolumeId)
	}
	jobVolumeID := *volumesOutput.Volumes[0].VolumeId
	s.logger.Info().Msgf("CreateSnapshot: Found volume ID %s for device %s", jobVolumeID, devicePath)

	// 2. Operations on jobVolumeID
	s.logger.Info().Msgf("CreateSnapshot: Stopping docker service...")
	if _, err := s.runCommand(ctx, "sudo", "systemctl", "stop", "docker"); err != nil {
		s.logger.Warn().Msgf("Warning: failed to stop docker (may not be running or installed): %v", err)
	}

	s.logger.Info().Msgf("CreateSnapshot: Unmounting %s (from device %s, volume %s)...", mountPoint, devicePath, jobVolumeID)
	if _, err := s.runCommand(ctx, "sudo", "umount", mountPoint); err != nil {
		dfOutput, checkErr := s.runCommand(ctx, "df", mountPoint)
		if checkErr == nil && strings.Contains(string(dfOutput), mountPoint) { // If still mounted, then error
			return nil, fmt.Errorf("failed to unmount %s: %w. Output: %s", mountPoint, err, string(dfOutput))
		}
		s.logger.Warn().Msgf("CreateSnapshot: Unmount of %s failed but it seems not mounted anymore: %v", mountPoint, err)
	} else {
		s.logger.Info().Msgf("CreateSnapshot: Successfully unmounted %s.", mountPoint)
	}

	s.logger.Info().Msgf("CreateSnapshot: Detaching volume %s...", jobVolumeID)
	_, err = s.ec2Client.DetachVolume(ctx, &ec2.DetachVolumeInput{
		VolumeId:   aws.String(jobVolumeID),
		InstanceId: aws.String(s.config.InstanceID),
	})
	if err != nil {
		descVol, descErr := s.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{jobVolumeID}})
		if descErr == nil && len(descVol.Volumes) > 0 {
			vol := descVol.Volumes[0]
			if vol.State == types.VolumeStateAvailable {
				s.logger.Info().Msgf("CreateSnapshot: DetachVolume call failed for %s, but volume is already %s. Proceeding.", jobVolumeID, vol.State)
			} else {
				return nil, fmt.Errorf("failed to initiate detach for volume %s (current state %s): %w", jobVolumeID, vol.State, err)
			}
		} else {
			return nil, fmt.Errorf("failed to initiate detach for volume %s: %w", jobVolumeID, err)
		}
	}

	volumeDetachedWaiter := ec2.NewVolumeAvailableWaiter(s.ec2Client) // Available state implies detached
	s.logger.Info().Msgf("CreateSnapshot: Waiting for volume %s to become available (detached)...", jobVolumeID)
	if err := volumeDetachedWaiter.Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{jobVolumeID}}, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("volume %s did not become available (detach) in time: %w", jobVolumeID, err)
	}
	s.logger.Info().Msgf("CreateSnapshot: Volume %s is detached.", jobVolumeID)

	// 3. Create new snapshot
	currentTime := time.Now()
	snapshotName := fmt.Sprintf("snapshot-%s-%s", s.config.GithubRef, currentTime.Format("20060102-150405"))
	s.logger.Info().Msgf("CreateSnapshot: Creating snapshot '%s' from volume %s for branch %s...", snapshotName, jobVolumeID, s.config.GithubRef)
	createSnapshotOutput, err := s.ec2Client.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId: aws.String(jobVolumeID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSnapshot,
				Tags: []types.Tag{
					{Key: aws.String(snapshotBranchTagKey), Value: aws.String(s.config.GithubRef)},
					{Key: aws.String(latestSnapshotTagKey), Value: aws.String("true")},
					{Key: aws.String(nameTagKey), Value: aws.String(snapshotName)},
					{Key: aws.String(timestampTagKey), Value: aws.String(currentTime.Format(time.RFC3339))},
				},
			},
		},
		Description: aws.String(fmt.Sprintf("Snapshot for branch %s taken at %s", s.config.GithubRef, currentTime.Format(time.RFC3339))),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot from volume %s: %w", jobVolumeID, err)
	}
	newSnapshotID := *createSnapshotOutput.SnapshotId
	s.logger.Info().Msgf("CreateSnapshot: Snapshot %s creation initiated. Waiting for completion...", newSnapshotID)

	snapshotCompletedWaiter := ec2.NewSnapshotCompletedWaiter(s.ec2Client)
	if err := snapshotCompletedWaiter.Wait(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{newSnapshotID}}, 30*time.Minute); err != nil {
		return nil, fmt.Errorf("snapshot %s did not complete in time: %w", newSnapshotID, err)
	}
	s.logger.Info().Msgf("CreateSnapshot: Snapshot %s completed.", newSnapshotID)

	// 4. Manage old 'latest' snapshots for this branch
	s.logger.Info().Msgf("CreateSnapshot: Finding old 'latest' snapshots for branch %s to untag...", gitBranch)
	oldSnapshotsOutput, err := s.ec2Client.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		Filters: []types.Filter{
			{Name: aws.String("tag:" + snapshotBranchTagKey), Values: []string{gitBranch}},
			{Name: aws.String("tag:" + latestSnapshotTagKey), Values: []string{"true"}},
		},
		OwnerIds: []string{"self"},
	})
	if err != nil {
		s.logger.Warn().Msgf("Warning: Failed to describe old snapshots for untagging: %v. Manual cleanup might be needed.", err)
	} else {
		for _, oldSnap := range oldSnapshotsOutput.Snapshots {
			if *oldSnap.SnapshotId != newSnapshotID {
				s.logger.Info().Msgf("CreateSnapshot: Removing '%s=true' tag from old snapshot %s", latestSnapshotTagKey, *oldSnap.SnapshotId)
				_, err := s.ec2Client.DeleteTags(ctx, &ec2.DeleteTagsInput{
					Resources: []string{*oldSnap.SnapshotId},
					Tags:      []types.Tag{{Key: aws.String(latestSnapshotTagKey), Value: aws.String("true")}},
				})
				if err != nil {
					s.logger.Warn().Msgf("Warning: Failed to remove '%s' tag from old snapshot %s: %v", latestSnapshotTagKey, *oldSnap.SnapshotId, err)
				}
			}
		}
	}

	// 5. Delete the jobVolumeID (the volume that was just snapshotted)
	s.logger.Info().Msgf("CreateSnapshot: Deleting original volume %s as its state is now in snapshot %s...", jobVolumeID, newSnapshotID)
	_, err = s.ec2Client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(jobVolumeID)})
	if err != nil {
		s.logger.Warn().Msgf("Warning: Failed to delete volume %s: %v. Manual cleanup may be required.", jobVolumeID, err)
	} else {
		s.logger.Info().Msgf("CreateSnapshot: Volume %s successfully deleted.", jobVolumeID)
	}

	return &CreateSnapshotOutput{SnapshotID: newSnapshotID}, nil
}

// runCommand executes a shell command and returns its combined output or an error.
// It now requires a context for potential cancellation if the command runs too long.
func (s *AWSSnapshotter) runCommand(ctx context.Context, name string, arg ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, arg...)
	s.logger.Info().Msgf("Executing command: %s %s", name, strings.Join(arg, " "))
	output, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.Warn().Msgf("Command failed: %s %s\nOutput:\n%s\nError: %v", name, strings.Join(arg, " "), string(output), err)
		return output, fmt.Errorf("command '%s %s' failed: %s: %w", name, strings.Join(arg, " "), string(output), err)
	}
	// Limit log output size for potentially verbose commands
	logOutput := string(output)
	if len(logOutput) > 200 {
		logOutput = logOutput[:200] + "... (output truncated)"
	}
	s.logger.Info().Msgf("Command successful. Output (first 200 chars or less):\n%s", logOutput)
	return output, nil
}

/*
func main() {
	// Setup logging to include timestamps and file/line information
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	ctx := context.Background()
	snap, err := NewAWSSnapshotter(ctx)
	if err != nil {
		log.Fatalf("Failed to create AWSSnapshotter: %v", err)
	}

	// Example: Set GITHUB_REF_NAME for testing
	// os.Setenv("GITHUB_REF_NAME", "feature/test-branch")

	mountPoint := "/mnt/docker_data" // Using a test mount point

	// Test RestoreSnapshot
	log.Println("---- TESTING RESTORE SNAPSHOT ----")
	restoreOutput, err := snap.RestoreSnapshot(ctx, mountPoint)
	if err != nil {
		log.Fatalf("Failed to restore snapshot: %v", err)
	}
	log.Printf("Snapshot restored. VolumeID: %s, DeviceName: %s mounted to %s", restoreOutput.VolumeID, restoreOutput.DeviceName, mountPoint)

	// Simulate some work being done
	log.Printf("Simulating work on %s... creating a test file.", mountPoint)
	// _, err = runCommand(ctx, "sudo", "touch", filepath.Join(mountPoint, "test_file_from_job.txt"))
	// if err != nil {
	// 	log.Printf("Failed to create test file: %v", err)
	// }

	// Test CreateSnapshot
	log.Println("---- TESTING CREATE SNAPSHOT ----")
	createOutput, err := snap.CreateSnapshot(ctx, mountPoint)
	if err != nil {
		log.Fatalf("Failed to create snapshot: %v", err)
	}
	log.Printf("Snapshot created: %s", createOutput.SnapshotID)

	log.Println("---- TEST COMPLETE ----")
}
*/
