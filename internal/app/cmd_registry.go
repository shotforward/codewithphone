package app

import "strings"

type runningCommand struct {
	run       backgroundCommandRun
	execution *commandExecution
}

func (s *Service) registerRunningCommand(run backgroundCommandRun, execution *commandExecution) {
	if strings.TrimSpace(run.CommandRunID) == "" || execution == nil {
		return
	}
	s.runningCommandsMu.Lock()
	defer s.runningCommandsMu.Unlock()
	s.runningCommands[run.CommandRunID] = runningCommand{
		run:       run,
		execution: execution,
	}
}

func (s *Service) releaseRunningCommand(commandRunID string) {
	if strings.TrimSpace(commandRunID) == "" {
		return
	}
	s.runningCommandsMu.Lock()
	defer s.runningCommandsMu.Unlock()
	delete(s.runningCommands, commandRunID)
}

func (s *Service) terminateRunningCommand(sessionID, commandRunID string) (backgroundCommandRun, bool) {
	if strings.TrimSpace(commandRunID) == "" {
		return backgroundCommandRun{}, false
	}
	s.runningCommandsMu.Lock()
	entry, ok := s.runningCommands[commandRunID]
	s.runningCommandsMu.Unlock()
	if !ok || entry.execution == nil {
		return backgroundCommandRun{}, false
	}
	if strings.TrimSpace(sessionID) != "" && strings.TrimSpace(entry.run.SessionID) != strings.TrimSpace(sessionID) {
		return backgroundCommandRun{}, false
	}
	entry.execution.terminate()
	return entry.run, true
}
