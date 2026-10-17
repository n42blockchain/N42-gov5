package sharedexec

import "errors"

// EnableOwnBuildReuse is a startup-only backend optimization, not a chain-rule
// change. Existing constructors/default callers continue executing imports.
func (s *Server) EnableOwnBuildReuse() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.resumeArming || len(s.jobs) != 0 || len(s.builds) != 0 || s.chain.Config().HotStuff == nil || len(s.chain.Config().HotStuff.Validators) != 7 {
		return errors.New("own-build reuse requires idle fixed-seven performance owner")
	}
	s.reuseOwnBuild = true
	return nil
}

func (s *Server) OwnBuildReuses() uint64 { return s.ownBuildReuses.Load() }
