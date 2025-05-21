package snapshot

import "runtime"

func (s *AWSSnapshotter) Arch() string {
	return runtime.GOARCH
}

func (s *AWSSnapshotter) Platform() string {
	return runtime.GOOS
}
