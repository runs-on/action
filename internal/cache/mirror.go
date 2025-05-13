package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/rs/zerolog"
)

type Mirror struct {
	URL       string
	GitHubURL string
	RepoName  string
	MirrorDir string
	TargetDir string
	Ref       string
	Token     string
}

func NewMirror(mirror string, token string) (*Mirror, error) {
	repoName := mirror
	ref := "main"
	if strings.Contains(mirror, "@") {
		parts := strings.Split(mirror, "@")
		repoName = parts[0]
		ref = parts[1]
	}
	mirrorDir := filepath.Join(os.TempDir(), fmt.Sprintf("runs-on-mirror-%s", strings.ReplaceAll(repoName, "/", "-")))
	targetDir := filepath.Join(fmt.Sprintf("mirrors/%s", repoName))
	if _, err := os.Stat(mirrorDir); os.IsNotExist(err) {
		err = os.MkdirAll(mirrorDir, 0755)
		if err != nil {
			return nil, err
		}
	}
	return &Mirror{
		URL:       mirror,
		GitHubURL: fmt.Sprintf("https://github.com/%s", repoName),
		RepoName:  repoName,
		MirrorDir: mirrorDir,
		TargetDir: targetDir,
		Ref:       ref,
		Token:     token,
	}, nil
}

func (m *Mirror) MirrorOrUpdate(ctx context.Context, logger *zerolog.Logger) error {
	logger.Info().Msgf("Cloning or updating git repository: %s", m.URL)
	mustMirror := false

	repo, err := git.PlainOpen(m.MirrorDir)
	if err != nil {
		logger.Warn().Msgf("Failed to open git repository: %s", err)

		err = os.RemoveAll(m.MirrorDir)
		if err != nil {
			logger.Error().Msgf("Failed to remove mirror directory: %s", err)
			return err
		}
		mustMirror = true
	}

	if mustMirror {
		repo, err = m.Mirror(ctx, logger)
		if err != nil {
			logger.Error().Msgf("Failed to clone git repository: %s", err)
			return err
		}
	}

	logger.Info().Msgf("Fetching remote refs: %s", m.Ref)
	err = repo.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("refs/heads/*:refs/remotes/origin/*")),
		},
		Auth: m.Auth(),
	})
	if err != nil {
		if err == git.NoErrAlreadyUpToDate {
			logger.Info().Msgf("Git repository already up to date: %s", m.URL)
			return nil
		}
		logger.Error().Msgf("Failed to fetch git repository: %s", err)
		return err
	}
	return nil
}

func (m *Mirror) Auth() *http.BasicAuth {
	if m.Token == "" {
		return nil
	}
	return &http.BasicAuth{
		Username: "x-access-token",
		Password: m.Token,
	}
}

func (m *Mirror) Mirror(ctx context.Context, logger *zerolog.Logger) (*git.Repository, error) {
	logger.Info().Msgf("Mirroring git repository: %s", m.URL)
	repo, err := git.PlainClone(m.MirrorDir, false, &git.CloneOptions{
		URL:      m.GitHubURL,
		Progress: os.Stdout,
		Mirror:   true,
		Auth:     m.Auth(),
	})
	if err != nil {
		return nil, err
	}
	return repo, nil
}
func (m *Mirror) Checkout(ctx context.Context, logger *zerolog.Logger) error {
	logger.Info().Msgf("Opening git repository: %s", m.MirrorDir)

	mirrorRepo, err := git.PlainOpen(m.MirrorDir)
	if err != nil {
		return err
	}

	var hash *plumbing.Hash
	if len(m.Ref) >= 4 && len(m.Ref) <= 40 {
		// Try to resolve short/full SHA
		if h, err := mirrorRepo.ResolveRevision(plumbing.Revision(m.Ref)); err == nil {
			hash = h
		}
	}

	var ref string
	if hash == nil && m.Ref != "" {
		if strings.HasPrefix(m.Ref, "refs/tags/") {
			ref = m.Ref
		} else {
			ref = "refs/remotes/origin/" + m.Ref
		}
	} else if hash == nil {
		ref = "refs/remotes/origin/HEAD"
	}

	logger.Info().Msgf("Checking out git reference: %s", ref)

	// Get the reference or hash
	var revision plumbing.Hash
	if hash != nil {
		revision = *hash
	} else {
		ref, err := mirrorRepo.Reference(plumbing.ReferenceName(ref), true)
		if err != nil {
			return err
		}
		revision = ref.Hash()
	}

	logger.Info().Msgf("Checking out git commit: %s", revision.String())

	_, err = git.PlainClone(m.TargetDir, false, &git.CloneOptions{
		URL:           "file://" + m.MirrorDir,
		Progress:      os.Stdout,
		Depth:         1,
		ReferenceName: plumbing.ReferenceName(ref),
	})
	if err != nil {
		return err
	}

	return nil
}
