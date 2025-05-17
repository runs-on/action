package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/rs/zerolog"
	runsOnConfig "github.com/runs-on/action/internal/config"
)

const (
	// Tags used for resource identification
	snapshotBranchTagKey     = "runs-on-snapshot-branch"
	snapshotRepositoryTagKey = "runs-on-snapshot-repository"
	nameTagKey               = "Name"
	timestampTagKey          = "runs-on-timestamp"

	// Default Volume Specifications
	defaultVolumeSizeGiB            int32 = 40
	defaultVolumeType                     = types.VolumeTypeGp3
	defaultVolumeIops               int32 = 3000
	defaultVolumeThroughputMBps     int32 = 125
	defaultVolumeInitializationRate int32 = 300
	suggestedDeviceName                   = "/dev/sdf" // AWS might assign /dev/xvdf etc.

	defaultVolumeInUseMaxWaitTime       = 5 * time.Minute
	defaultVolumeAvailableMaxWaitTime   = 5 * time.Minute
	defaultSnapshotCompletedMaxWaitTime = 10 * time.Minute
)

var defaultSnapshotCompletedWaiterOptions = func(o *ec2.SnapshotCompletedWaiterOptions) {
	o.MaxDelay = 10 * time.Second
	o.MinDelay = 5 * time.Second
}

var defaultVolumeInUseWaiterOptions = func(o *ec2.VolumeInUseWaiterOptions) {
	o.MaxDelay = 6 * time.Second
	o.MinDelay = 3 * time.Second
}

var defaultVolumeAvailableWaiterOptions = func(o *ec2.VolumeAvailableWaiterOptions) {
	o.MaxDelay = 6 * time.Second
	o.MinDelay = 3 * time.Second
}

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

// VolumeInfo stores information about the mounted volume
type VolumeInfo struct {
	VolumeID     string `json:"volume_id"`
	DeviceName   string `json:"device_name"`
	MountPoint   string `json:"mount_point"`
	AttachmentID string `json:"attachment_id,omitempty"`
}

// AWSSnapshotter provides methods to manage EBS snapshots and volumes.
type AWSSnapshotter struct {
	logger    *zerolog.Logger
	config    SnapshotterConfig
	ec2Client *ec2.Client
}

type SnapshotterConfig struct {
	Version                   string
	GithubRef                 string
	GithubRepository          string
	InstanceID                string
	Az                        string
	WaitForSnapshotCompletion bool
	DefaultBranch             string
	CustomTags                []runsOnConfig.Tag
	SnapshotName              string
	VolumeName                string
}

// NewAWSSnapshotter creates a new AWSSnapshotter instance.
// It initializes the AWS SDK configuration and fetches EC2 instance metadata.
func NewAWSSnapshotter(ctx context.Context, logger *zerolog.Logger, cfg SnapshotterConfig) (*AWSSnapshotter, error) {
	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS SDK config: %w", err)
	}

	if cfg.Version == "" {
		cfg.Version = "v1"
	}

	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("instanceID is required")
	}

	if cfg.Az == "" {
		return nil, fmt.Errorf("az is required")
	}

	if cfg.GithubRepository == "" {
		return nil, fmt.Errorf("githubRepository is required")
	}

	if cfg.GithubRef == "" {
		return nil, fmt.Errorf("githubRef is required")
	}

	if cfg.CustomTags == nil {
		cfg.CustomTags = []runsOnConfig.Tag{}
	}

	sanitizedGithubRef := strings.TrimPrefix(cfg.GithubRef, "refs/heads/")
	sanitizedGithubRef = strings.ReplaceAll(sanitizedGithubRef, "/", "-")
	if len(sanitizedGithubRef) > 40 {
		sanitizedGithubRef = sanitizedGithubRef[:40]
	}

	currentTime := time.Now()
	if cfg.SnapshotName == "" {
		cfg.SnapshotName = fmt.Sprintf("runs-on-snapshot-%s-%s", sanitizedGithubRef, currentTime.Format("20060102-150405"))
	}

	if cfg.VolumeName == "" {
		cfg.VolumeName = fmt.Sprintf("runs-on-volume-%s-%s", sanitizedGithubRef, currentTime.Format("20060102-150405"))
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

// getVolumeInfoPath returns the path to the volume info JSON file for a given mount point
func getVolumeInfoPath(mountPoint string) string {
	// Replace slashes with hyphens and remove leading/trailing hyphens
	sanitizedPath := strings.Trim(strings.ReplaceAll(mountPoint, "/", "-"), "-")
	return filepath.Join("/runs-on", fmt.Sprintf("snapshot-%s.json", sanitizedPath))
}

// saveVolumeInfo writes volume information to a JSON file
func (s *AWSSnapshotter) saveVolumeInfo(volumeInfo *VolumeInfo) error {
	infoPath := getVolumeInfoPath(volumeInfo.MountPoint)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(infoPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for volume info: %w", err)
	}

	data, err := json.MarshalIndent(volumeInfo, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal volume info: %w", err)
	}

	if err := os.WriteFile(infoPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write volume info file: %w", err)
	}

	return nil
}

// loadVolumeInfo reads volume information from a JSON file
func (s *AWSSnapshotter) loadVolumeInfo(mountPoint string) (*VolumeInfo, error) {
	infoPath := getVolumeInfoPath(mountPoint)
	data, err := os.ReadFile(infoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read volume info file: %w", err)
	}

	var volumeInfo VolumeInfo
	if err := json.Unmarshal(data, &volumeInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal volume info: %w", err)
	}

	return &volumeInfo, nil
}

func (s *AWSSnapshotter) getSnapshotTagValue() string {
	return fmt.Sprintf("%s-%s", s.config.Version, s.config.GithubRef)
}

func (s *AWSSnapshotter) getSnapshotTagValueDefaultBranch() string {
	return fmt.Sprintf("%s-%s", s.config.Version, s.config.DefaultBranch)
}

// RestoreSnapshot finds the latest snapshot for the current git branch,
// creates a volume from it (or a new volume if no snapshot exists),
// attaches it to the instance, and mounts it to the specified mountPoint.
func (s *AWSSnapshotter) RestoreSnapshot(ctx context.Context, mountPoint string) (*RestoreSnapshotOutput, error) {
	gitBranch := s.config.GithubRef
	s.logger.Info().Msgf("RestoreSnapshot: Using git ref: %s", gitBranch)

	var err error

	var newVolume *types.Volume
	var volumeIsNewAndUnformatted bool
	// 1. Find latest snapshot for branch
	filters := []types.Filter{
		{Name: aws.String("tag:" + snapshotBranchTagKey), Values: []string{s.getSnapshotTagValue()}},
		{Name: aws.String("tag:" + snapshotRepositoryTagKey), Values: []string{s.config.GithubRepository}},
		{Name: aws.String("status"), Values: []string{string(types.SnapshotStateCompleted)}},
	}
	for _, tag := range s.config.CustomTags {
		filters = append(filters, types.Filter{Name: aws.String(fmt.Sprintf("tag:%s", tag.Key)), Values: []string{tag.Value}})
	}
	s.logger.Info().Msgf("RestoreSnapshot: Searching for the latest snapshot for branch: %s and tags: %v", gitBranch, filters)
	snapshotsOutput, err := s.ec2Client.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		Filters:  filters,
		OwnerIds: []string{"self"}, // Or specific account ID if needed
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe snapshots for branch %s: %w", gitBranch, err)
	}

	var latestSnapshot *types.Snapshot
	if len(snapshotsOutput.Snapshots) > 0 {
		// Find most recent snapshot by comparing timestamps
		latestSnapshot = &snapshotsOutput.Snapshots[0]
		for _, snap := range snapshotsOutput.Snapshots {
			if snapTime := snap.StartTime; snapTime.After(*latestSnapshot.StartTime) {
				latestSnapshot = &snap
			}
		}
		s.logger.Info().Msgf("RestoreSnapshot: Found latest snapshot %s for branch %s", *latestSnapshot.SnapshotId, gitBranch)
	} else if s.config.DefaultBranch != "" {
		// Try finding snapshot from default branch
		filters[0] = types.Filter{Name: aws.String("tag:" + snapshotBranchTagKey), Values: []string{s.getSnapshotTagValueDefaultBranch()}}
		s.logger.Info().Msgf("RestoreSnapshot: No snapshot found for branch %s, trying default branch %s with tags: %v", gitBranch, s.config.DefaultBranch, filters)

		defaultBranchSnapshotsOutput, err := s.ec2Client.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
			Filters:  filters,
			OwnerIds: []string{"self"},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to describe snapshots for default branch %s: %w", s.config.DefaultBranch, err)
		}

		if len(defaultBranchSnapshotsOutput.Snapshots) > 0 {
			latestSnapshot = &defaultBranchSnapshotsOutput.Snapshots[0]
			for _, snap := range defaultBranchSnapshotsOutput.Snapshots {
				if snapTime := snap.StartTime; snapTime.After(*latestSnapshot.StartTime) {
					latestSnapshot = &snap
				}
			}
			s.logger.Info().Msgf("RestoreSnapshot: Found latest snapshot %s from default branch %s", *latestSnapshot.SnapshotId, s.config.DefaultBranch)
		} else {
			s.logger.Info().Msgf("RestoreSnapshot: No existing snapshot found for branch %s or default branch %s. A new volume will be created.", gitBranch, s.config.DefaultBranch)
		}
	}

	commonVolumeTags := []types.Tag{
		{Key: aws.String(snapshotBranchTagKey), Value: aws.String(s.getSnapshotTagValue())},
		{Key: aws.String(snapshotRepositoryTagKey), Value: aws.String(s.config.GithubRepository)},
		{Key: aws.String(nameTagKey), Value: aws.String(s.config.VolumeName)},
	}
	for _, tag := range s.config.CustomTags {
		commonVolumeTags = append(commonVolumeTags, types.Tag{Key: aws.String(tag.Key), Value: aws.String(tag.Value)})
	}

	// Use snapshot only if its size is at least the default volume size, otherwise create a new volume
	if latestSnapshot != nil && latestSnapshot.VolumeSize != nil && *latestSnapshot.VolumeSize >= defaultVolumeSizeGiB {
		// 2. Create Volume from Snapshot
		s.logger.Info().Msgf("RestoreSnapshot: Creating volume from snapshot %s", *latestSnapshot.SnapshotId)
		createVolumeOutput, err := s.ec2Client.CreateVolume(ctx, &ec2.CreateVolumeInput{
			SnapshotId:       latestSnapshot.SnapshotId,
			AvailabilityZone: aws.String(s.config.Az),
			VolumeType:       defaultVolumeType,
			// Size is determined by snapshot, but can be increased. Ensure it meets min throughput if increasing.
			// Size:             aws.Int32(defaultVolumeSizeGiB),
			Iops:                     aws.Int32(defaultVolumeIops),
			VolumeInitializationRate: aws.Int32(defaultVolumeInitializationRate),
			Throughput:               aws.Int32(defaultVolumeThroughputMBps),
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
		s.logger.Info().Msgf("RestoreSnapshot: Deferring cleanup of volume %s", *newVolume.VolumeId)
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
	volumeAvailableWaiter := ec2.NewVolumeAvailableWaiter(s.ec2Client, defaultVolumeAvailableWaiterOptions)
	if err := volumeAvailableWaiter.Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{*newVolume.VolumeId}}, defaultVolumeAvailableMaxWaitTime); err != nil {
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

	volumeInUseWaiter := ec2.NewVolumeInUseWaiter(s.ec2Client, defaultVolumeInUseWaiterOptions)
	err = volumeInUseWaiter.Wait(ctx, &ec2.DescribeVolumesInput{
		VolumeIds: []string{*newVolume.VolumeId},
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.status"),
				Values: []string{"attached"},
			},
		},
	}, defaultVolumeInUseMaxWaitTime)
	if err != nil {
		return nil, fmt.Errorf("volume %s did not attach successfully and current state unknown: %w", *newVolume.VolumeId, err)
	}
	// Fetch volume details again to confirm device name, as the attachOutput.Device might be a suggestion
	// and the waiter confirms attachment, not necessarily the final device name if it changed.
	descVolOutput, descErr := s.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{*newVolume.VolumeId}})
	s.logger.Info().Msgf("RestoreSnapshot: Volume %s attachments: %v", *newVolume.VolumeId, descVolOutput.Volumes[0].Attachments)
	if descErr == nil && len(descVolOutput.Volumes) > 0 && len(descVolOutput.Volumes[0].Attachments) > 0 {
		actualDeviceName = *descVolOutput.Volumes[0].Attachments[0].Device
	} else {
		return nil, fmt.Errorf("volume %s did not attach successfully and current state unknown: %w", *newVolume.VolumeId, err)
	}
	s.logger.Info().Msgf("RestoreSnapshot: Volume %s attached as %s.", *newVolume.VolumeId, actualDeviceName)

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
	for _, line := range strings.Split(strings.TrimSpace(string(lsblkOutput)), "\n") {
		s.logger.Info().Msgf("RestoreSnapshot: lsblk output: %s", line)
		fields := strings.SplitN(line, " ", 2)
		s.logger.Info().Msgf("RestoreSnapshot: fields: %v", fields)
		// first volume is the root volume, so we need to skip it
		if len(fields) > 1 && fields[1] == "Amazon Elastic Block Store" {
			s.logger.Info().Msgf("RestoreSnapshot: Found volume: %s", fields[0])
			actualDeviceName = fields[0]
		}
	}
	s.logger.Info().Msgf("RestoreSnapshot: Actual device name: %s", actualDeviceName)

	// Save volume info to JSON file
	volumeInfo := &VolumeInfo{
		VolumeID:   *newVolume.VolumeId,
		DeviceName: actualDeviceName,
		MountPoint: mountPoint,
	}
	if err := s.saveVolumeInfo(volumeInfo); err != nil {
		s.logger.Warn().Msgf("RestoreSnapshot: Failed to save volume info: %v", err)
	}

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

	s.logger.Info().Msgf("RestoreSnapshot: Displaying docker disk usage...")
	if _, err := s.runCommand(ctx, "sudo", "docker", "system", "df"); err != nil {
		s.logger.Warn().Msgf("RestoreSnapshot: failed to display docker disk usage: %v. Docker snapshot may not be working so unmounting docker folder.", err)
		// Try to unmount docker folder on error
		if _, err := s.runCommand(ctx, "sudo", "umount", "/var/lib/docker"); err != nil {
			s.logger.Warn().Msgf("RestoreSnapshot: failed to unmount docker folder: %v", err)
		}
		return nil, fmt.Errorf("failed to display docker disk usage: %w", err)
	}
	s.logger.Info().Msgf("RestoreSnapshot: Docker disk usage displayed.")

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
	s.logger.Info().Msgf("CreateSnapshot: Using git ref: %s, Instance ID: %s, MountPoint: %s", gitBranch, s.config.InstanceID, mountPoint)

	// Load volume info from JSON file
	volumeInfo, err := s.loadVolumeInfo(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("failed to load volume info: %w", err)
	}

	// 2. Operations on jobVolumeID
	s.logger.Info().Msgf("CreateSnapshot: Cleaning up useless files...")
	if _, err := s.runCommand(ctx, "sudo", "docker", "builder", "prune", "-f"); err != nil {
		s.logger.Warn().Msgf("Warning: failed to prune docker builder: %v", err)
	}

	s.logger.Info().Msgf("CreateSnapshot: Stopping docker service...")
	if _, err := s.runCommand(ctx, "sudo", "systemctl", "stop", "docker"); err != nil {
		s.logger.Warn().Msgf("Warning: failed to stop docker (may not be running or installed): %v", err)
	}

	s.logger.Info().Msgf("CreateSnapshot: Unmounting %s (from device %s, volume %s)...", mountPoint, volumeInfo.DeviceName, volumeInfo.VolumeID)
	if _, err := s.runCommand(ctx, "sudo", "umount", mountPoint); err != nil {
		dfOutput, checkErr := s.runCommand(ctx, "df", mountPoint)
		if checkErr == nil && strings.Contains(string(dfOutput), mountPoint) { // If still mounted, then error
			return nil, fmt.Errorf("failed to unmount %s: %w. Output: %s", mountPoint, err, string(dfOutput))
		}
		s.logger.Warn().Msgf("CreateSnapshot: Unmount of %s failed but it seems not mounted anymore: %v", mountPoint, err)
	} else {
		s.logger.Info().Msgf("CreateSnapshot: Successfully unmounted %s.", mountPoint)
	}

	s.logger.Info().Msgf("CreateSnapshot: Detaching volume %s...", volumeInfo.VolumeID)
	_, err = s.ec2Client.DetachVolume(ctx, &ec2.DetachVolumeInput{
		VolumeId:   aws.String(volumeInfo.VolumeID),
		InstanceId: aws.String(s.config.InstanceID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initiate detach for volume %s: %w", volumeInfo.VolumeID, err)
	}

	volumeDetachedWaiter := ec2.NewVolumeAvailableWaiter(s.ec2Client, defaultVolumeAvailableWaiterOptions) // Available state implies detached
	s.logger.Info().Msgf("CreateSnapshot: Waiting for volume %s to become available (detached)...", volumeInfo.VolumeID)
	if err := volumeDetachedWaiter.Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeInfo.VolumeID}}, defaultVolumeAvailableMaxWaitTime); err != nil {
		return nil, fmt.Errorf("volume %s did not become available (detach) in time: %w", volumeInfo.VolumeID, err)
	}
	s.logger.Info().Msgf("CreateSnapshot: Volume %s is detached.", volumeInfo.VolumeID)

	// 3. Create new snapshot
	currentTime := time.Now()
	s.logger.Info().Msgf("CreateSnapshot: Creating snapshot '%s' from volume %s for branch %s...", s.config.SnapshotName, volumeInfo.VolumeID, s.config.GithubRef)
	snapshotTags := []types.Tag{
		{Key: aws.String(snapshotBranchTagKey), Value: aws.String(s.getSnapshotTagValue())},
		{Key: aws.String(snapshotRepositoryTagKey), Value: aws.String(s.config.GithubRepository)},
		{Key: aws.String(nameTagKey), Value: aws.String(s.config.SnapshotName)},
	}
	for _, tag := range s.config.CustomTags {
		snapshotTags = append(snapshotTags, types.Tag{Key: aws.String(tag.Key), Value: aws.String(tag.Value)})
	}
	createSnapshotOutput, err := s.ec2Client.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId: aws.String(volumeInfo.VolumeID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSnapshot,
				Tags:         snapshotTags,
			},
		},
		Description: aws.String(fmt.Sprintf("Snapshot for branch %s taken at %s", s.config.GithubRef, currentTime.Format(time.RFC3339))),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot from volume %s: %w", volumeInfo.VolumeID, err)
	}
	newSnapshotID := *createSnapshotOutput.SnapshotId
	s.logger.Info().Msgf("CreateSnapshot: Snapshot %s creation initiated.", newSnapshotID)

	if !s.config.WaitForSnapshotCompletion {
		s.logger.Info().Msgf("CreateSnapshot: not waiting for snapshot completion, returning immediately.")
		return &CreateSnapshotOutput{SnapshotID: newSnapshotID}, nil
	}

	s.logger.Info().Msgf("CreateSnapshot: Waiting for snapshot %s completion...", newSnapshotID)
	snapshotCompletedWaiter := ec2.NewSnapshotCompletedWaiter(s.ec2Client, defaultSnapshotCompletedWaiterOptions)
	if err := snapshotCompletedWaiter.Wait(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{newSnapshotID}}, defaultSnapshotCompletedMaxWaitTime); err != nil {
		return nil, fmt.Errorf("snapshot %s did not complete in time: %w", newSnapshotID, err)
	}
	s.logger.Info().Msgf("CreateSnapshot: Snapshot %s completed.", newSnapshotID)

	// 5. Delete the jobVolumeID (the volume that was just snapshotted)
	s.logger.Info().Msgf("CreateSnapshot: Deleting original volume %s as its state is now in snapshot %s...", volumeInfo.VolumeID, newSnapshotID)
	_, err = s.ec2Client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeInfo.VolumeID)})
	if err != nil {
		s.logger.Warn().Msgf("Warning: Failed to delete volume %s: %v. Manual cleanup may be required.", volumeInfo.VolumeID, err)
	} else {
		s.logger.Info().Msgf("CreateSnapshot: Volume %s successfully deleted.", volumeInfo.VolumeID)
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
	if len(logOutput) > 400 {
		logOutput = logOutput[:200] + "... (output truncated)"
	}
	s.logger.Info().Msgf("Command successful. Output (first 200 chars or less):\n%s", logOutput)
	return output, nil
}
