package kopia

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/kopia/kopia/fs/localfs"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/blob/s3"
	"github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/policy"
	"github.com/kopia/kopia/snapshot/restore"
	"github.com/kopia/kopia/snapshot/snapshotfs"
	"github.com/rs/zerolog"
)

const (
	KOPIA_HOSTNAME = "github-actions"
	KOPIA_USER     = "runner"
)

type Config struct {
	Region   string
	Prefix   string
	S3Bucket string
	Password string
}

func kopiaConfigPath() string {
	return "/tmp/kopia-config.tmp"
}

type KopiaClient struct {
	logger *zerolog.Logger
	config *Config
}

func NewKopiaClient(ctx context.Context, logger *zerolog.Logger, config *Config) (*KopiaClient, error) {

	if config.S3Bucket == "" {
		return nil, fmt.Errorf("S3Bucket must be set")
	}
	if config.Region == "" {
		return nil, fmt.Errorf("Region must be set")
	}

	st, err := s3.New(ctx, &s3.Options{
		BucketName:     config.S3Bucket,
		Prefix:         config.Prefix,
		Region:         config.Region,
		Endpoint:       fmt.Sprintf("s3.%s.amazonaws.com", config.Region),
		DoNotUseTLS:    false,
		DoNotVerifyTLS: false,
	}, false) // 'false' for isCreate

	if err != nil {
		return nil, fmt.Errorf("failed to create Kopia S3 storage: %w", err)
	}
	defer st.Close(ctx)

	lc := &repo.LocalConfig{
		Storage: &blob.ConnectionInfo{
			Type:   "s3",
			Config: map[string]string{"bucket": config.S3Bucket, "prefix": config.Prefix, "region": config.Region, "endpoint": fmt.Sprintf("s3.%s.amazonaws.com", config.Region)},
		},
	}
	// dump the local config to a file, using JSON
	lcJSON, err := json.Marshal(lc)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal local config: %w", err)
	}
	err = os.WriteFile(kopiaConfigPath(), lcJSON, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write local config: %w", err)
	}

	// Check if repo exists, create if not
	if err := repo.Initialize(ctx, st, &repo.NewRepositoryOptions{}, config.Password); err != nil {
		if !strings.Contains(err.Error(), "repository already initialized") {
			return nil, fmt.Errorf("failed to initialize Kopia repository: %w", err)
		}
		logger.Info().Msg("Kopia repository already exists.")
	} else {
		logger.Info().Msg("Kopia repository initialized successfully.")
	}

	return &KopiaClient{logger: logger, config: config}, nil
}

func (c *KopiaClient) Open(ctx context.Context) (repo.Repository, error) {
	// Connect to the repository
	rep, err := repo.Open(ctx, kopiaConfigPath(), c.config.Password, &repo.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open Kopia repository: %w", err)
	}
	return rep, nil
}

// RestoreSnapshot restores the latest Kopia snapshot for a predefined source/target.
func (c *KopiaClient) Restore(ctx context.Context, directory string) error {
	c.logger.Info().Str("snapshot_directory", directory).Str("snapshot_path", directory).Msg("Executing Kopia snapshot restore...")

	err := c.preRestore(ctx, directory)
	if err != nil {
		return fmt.Errorf("failed to execute pre-restore: %w", err)
	}

	rep, err := c.Open(ctx)
	if err != nil {
		return fmt.Errorf("failed to open Kopia repository: %w", err)
	}
	defer rep.Close(ctx)

	// Find the latest snapshot for the given source path
	sourceInfo, err := snapshot.ParseSourceInfo(directory, KOPIA_HOSTNAME, KOPIA_USER)
	if err != nil {
		return fmt.Errorf("failed to parse Kopia source info: %w", err)
	}

	snapshots, err := snapshot.ListSnapshots(ctx, rep, sourceInfo)
	if err != nil {
		return fmt.Errorf("failed to list Kopia snapshots: %w", err)
	}

	if len(snapshots) == 0 {
		c.logger.Info().Str("snapshot_name", directory).Msg("No Kopia snapshots found for source, skipping restore.")
		return nil // No snapshot to restore
	}

	// Find the latest manifest
	var latestManifest *snapshot.Manifest
	for _, m := range snapshots {
		if latestManifest == nil || m.StartTime.After(latestManifest.StartTime) {
			latestManifest = m
		}
	}

	c.logger.Info().
		Interface("snapshotID", latestManifest.ID).
		Interface("source", latestManifest.Source).
		Time("startTime", latestManifest.StartTime.ToTime()).
		Msg("Found latest Kopia snapshot to restore.")

	// Ensure the target directory is clean before restoring
	// do not remove the target directory, because it might be a tmpfs (e.g. /var/lib/docker)
	// logger.Info().Str("targetDir", targetDir).Msg("Removing existing target directory before restore...")
	// if err := os.RemoveAll(targetDir); err != nil {
	// 	// If removal fails, it might indicate a permissions issue or other problem.
	// 	// It's safer to stop the restore process here.
	// 	return fmt.Errorf("failed to remove existing target directory %s before restore: %w", targetDir, err)
	// }
	// logger.Info().Str("targetDir", targetDir).Msg("Target directory cleared successfully.")

	// Initialize our simple progress reporter
	progressReporter := newSimpleRestoreProgressReporter(1500*time.Millisecond, os.Stderr)
	// Defer finish *after* the potential final callback call
	defer progressReporter.finish()

	// Restore the snapshot
	// Create restore output targetting the filesystem directory
	filesystemOutput := &restore.FilesystemOutput{
		TargetPath:             directory,
		OverwriteDirectories:   true,  // Default from CLI
		OverwriteFiles:         true,  // Default from CLI
		OverwriteSymlinks:      true,  // Default from CLI
		IgnorePermissionErrors: false, // Default from CLI (c.restoreIgnorePermissionErrors)
		WriteFilesAtomically:   false, // Default from CLI (c.restoreWriteFilesAtomically)
		SkipOwners:             true,  // Default from CLI (c.restoreSkipOwners)
		SkipPermissions:        false, // Default from CLI (c.restoreSkipPermissions)
		SkipTimes:              true,  // Default from CLI (c.restoreSkipTimes)
		WriteSparseFiles:       false, // Default from CLI (c.restoreWriteSparseFiles)
	}
	if err := filesystemOutput.Init(ctx); err != nil {
		return fmt.Errorf("unable to initialize filesystem output for target dir: %w", err)
	}

	// Get the root entry of the snapshot
	snapshotRootEntry, err := snapshotfs.SnapshotRoot(rep, latestManifest)
	if err != nil {
		return fmt.Errorf("unable to get snapshot root entry: %w", err)
	}

	// Restore.Snapshot is deprecated, use restore.Entry
	stats, err := restore.Entry(ctx, rep, filesystemOutput, snapshotRootEntry, restore.Options{
		RestoreDirEntryAtDepth: math.MaxInt32,
		Parallel:               int(math.Max(float64(runtime.NumCPU()*2), 12)),
		Incremental:            true,
		IgnoreErrors:           false,
		MinSizeForPlaceholder:  0,
		ProgressCallback:       progressReporter.callbackRestore,
	})
	if err != nil {
		// Finish won't necessarily clear the line on error, but maybe it should?
		// Consider if finish() should be called here too if an error occurs during restore.
		// For now, it only clears on successful return path via defer.
		return fmt.Errorf("failed to restore Kopia snapshot %s: %w", string(latestManifest.ID), err)
	}

	// Explicitly call callback with final stats before finish() runs
	progressReporter.callbackRestore(ctx, stats)

	c.logger.Info().
		Str("snapshot_id", string(latestManifest.ID)).
		Str("snapshot_directory", directory).
		Str("snapshot_path", directory).
		Interface("snapshot_stats", stats).
		Msg("Kopia snapshot restored successfully.")

	err = c.postRestore(ctx, directory)
	if err != nil {
		return fmt.Errorf("failed to execute post-restore: %w", err)
	}
	return nil
}

// CreateOrUpdateSnapshot creates or updates a Kopia snapshot for a predefined source/target.
func (c *KopiaClient) Snapshot(ctx context.Context, directory string, usePreviousManifests bool) error {
	c.logger.Info().Str("snapshot_directory", directory).Str("snapshot_path", directory).Msg("Executing Kopia snapshot creation...")

	err := c.preSnapshot(ctx, directory)
	if err != nil {
		return fmt.Errorf("failed to execute pre-snapshot: %w", err)
	}

	rep, err := c.Open(ctx)
	if err != nil {
		return fmt.Errorf("failed to open Kopia repository: %w", err)
	}
	defer rep.Close(ctx)

	sourceInfo, err := snapshot.ParseSourceInfo(directory, KOPIA_HOSTNAME, KOPIA_USER)
	if err != nil {
		return fmt.Errorf("failed to parse Kopia source info for snapshotting: %w", err)
	}

	// Use WriteSession for snapshot creation
	err = repo.WriteSession(ctx, rep, repo.WriteSessionOptions{
		Purpose: "Create snapshot",
		// Consider setting OnUpload if progress tracking is needed
	}, func(ctx context.Context, w repo.RepositoryWriter) error {
		// Get the local directory entry
		localEntry, err := localfs.NewEntry(directory)
		if err != nil {
			return fmt.Errorf("failed to get local directory entry '%s': %w", directory, err)
		}

		parallelUploads := 12
		// maxParallelFileReads := policy.OptionalInt(parallelUploads)
		// parallelUploadAboveSize := policy.OptionalInt64(10) // 10MB
		// maxParallelSnapshots := policy.OptionalInt(1)
		// policyOverride := policy.Policy{
		// 	CompressionPolicy: policy.CompressionPolicy{
		// 		CompressorName: "zstd-fastest",
		// 	},
		// 	UploadPolicy: policy.UploadPolicy{
		// 		MaxParallelFileReads:    &maxParallelFileReads,
		// 		ParallelUploadAboveSize: &parallelUploadAboveSize,
		// 		MaxParallelSnapshots:    &maxParallelSnapshots,
		// 	},
		// }
		policyTree, err := policy.TreeForSource(ctx, rep, sourceInfo)
		if err != nil {
			return fmt.Errorf("failed to get policy tree: %w", err)
		}

		previousManifestIDs, err := snapshot.ListSnapshotManifests(ctx, rep, &sourceInfo, nil)
		if err != nil {
			return fmt.Errorf("failed to list previous manifests: %w", err)
		}

		// Load previous manifests from IDs
		var previousManifests []*snapshot.Manifest
		for _, manID := range previousManifestIDs {
			m, err := snapshot.LoadSnapshot(ctx, rep, manID)
			if err != nil {
				// Log error but continue, might be ok if one manifest is unloadable
				c.logger.Warn().Err(err).Str("manifestID", string(manID)).Msg("Failed to load previous manifest, skipping.")
				continue
			}
			previousManifests = append(previousManifests, m)
		}

		progressReporter := newSimpleRestoreProgressReporter(1500*time.Millisecond, os.Stderr)
		defer progressReporter.finish()

		uploader := snapshotfs.NewUploader(w)
		uploader.ParallelUploads = parallelUploads
		uploader.ForceHashPercentage = 0
		uploader.CheckpointInterval = 30 * time.Minute
		uploader.Progress = newSnapshotProgressAdapter(ctx, progressReporter.callbackSnapshot, 1500*time.Millisecond)

		if usePreviousManifests {
			c.logger.Info().Interface("previousManifests", previousManifests).Msg("Previous manifests")
		} else {
			previousManifests = nil
		}

		manifest, err := uploader.Upload(ctx, localEntry, policyTree, sourceInfo, previousManifests...)
		if err != nil {
			return fmt.Errorf("failed to upload Kopia snapshot: %w", err)
		}

		c.logger.Info().Interface("stats", manifest.Stats).Msg("Kopia snapshot manifest created successfully.")

		// Explicitly call callback with final stats before finish() runs
		progressReporter.callbackSnapshot(ctx, manifest.Stats)

		// Save the snapshot manifest
		snapID, err := snapshot.SaveSnapshot(ctx, w, manifest)
		if err != nil {
			return fmt.Errorf("failed to save Kopia snapshot manifest: %w", err)
		}

		// Flush is handled by WriteSession wrapper

		// Optionally run maintenance
		// Consider if this should be run always, periodically, or manually
		// maintenance.QuickMaintenance(ctx, w, maintenance.ModeQuick)

		c.logger.Info().
			Str("snapshot_id", string(snapID)).
			Str("snapshot_directory", directory).
			Str("snapshot_path", directory).
			Msg("Kopia snapshot created successfully.")

		err = c.postSnapshot(ctx, directory)
		if err != nil {
			return fmt.Errorf("failed to execute post-snapshot: %w", err)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to run Kopia snapshot session: %w", err)
	}

	return nil
}
