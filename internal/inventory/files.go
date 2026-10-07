package inventory

import (
	"context"

	"drool/internal/driver/sshdriver"
	"drool/internal/files"
	"drool/internal/model"
)

// OpenFS opens a filesystem session on a host through the same resolution
// as jobs: decrypted credentials, the pinned host key (required) and the
// jump host. It implements files.Opener.
func (s *Service) OpenFS(ctx context.Context, hostID string, elevated bool) (files.FS, error) {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, err
	}
	if c.Host.JumpOnly != 0 {
		return nil, &model.OpError{Class: model.ClassValidation, Message: c.Host.Name + " is a jump host only and is not managed"}
	}
	return sshdriver.OpenFS(ctx, s.pool, c.Target, c.Become, elevated)
}

var _ files.Opener = (*Service)(nil)
