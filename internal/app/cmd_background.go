package app

import (
	"fmt"
	"time"
)

const maxBackgroundCommandsPerSession = 2

type backgroundCommandRun struct {
	CommandRunID string
	SessionID    string
	TaskRunID    string
	Command      string
	CWD          string
	LogPath      string
	PID          int
	StartedAt    time.Time
}

func (s *Service) reserveBackgroundCommand(run backgroundCommandRun) error {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()

	active := 0
	for _, candidate := range s.backgroundCommands {
		if candidate.SessionID == run.SessionID {
			active++
		}
	}
	if active >= maxBackgroundCommandsPerSession {
		return fmt.Errorf("too many background commands in this session (max=%d)", maxBackgroundCommandsPerSession)
	}
	s.backgroundCommands[run.CommandRunID] = run
	return nil
}

func (s *Service) releaseBackgroundCommand(commandRunID string) {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	delete(s.backgroundCommands, commandRunID)
}
