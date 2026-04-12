package app

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"errors"
	"strings"
	"syscall"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
)

func (s *Service) notifyMachineOffline() {
	s.notifyMachineOfflineWithToken(strings.TrimSpace(s.serverClient.MachineToken))
}

func (s *Service) ensureMachineBinding(ctx context.Context, hostname string, inventory machineInventory) error {
	mode, err := config.ParseBindMode(s.cfg.BindMode)
	if err != nil {
		return err
	}
	s.cfg.BindMode = mode
	switch mode {
	case config.BindModeForce:
		log.Printf("bind mode=%s: forcing device auth flow", mode)
		// Keep force mode runtime-only so restarts default back to auto.
		s.persistBindModeAuto()
		previousToken := strings.TrimSpace(s.serverClient.MachineToken)
		s.clearMachineToken()
		err := s.runDeviceAuthFlow(ctx, hostname)
		if err != nil && ctx.Err() != nil && previousToken != "" {
			log.Printf("binding interrupted before authorization; best-effort mark machine offline")
			s.notifyMachineOfflineWithToken(previousToken)
		}
		return err
	case config.BindModeTokenOnly:
		if strings.TrimSpace(s.cfg.MachineID) == "" || strings.TrimSpace(s.cfg.MachineToken) == "" {
			return fmt.Errorf("bind mode=%s requires existing machine_id and machine_token", mode)
		}
		log.Printf("bind mode=%s: using existing machine token only", mode)
		return nil
	case config.BindModeAuto:
		if strings.TrimSpace(s.cfg.MachineID) == "" || strings.TrimSpace(s.cfg.MachineToken) == "" {
			log.Printf("bind mode=%s: missing machine credentials, entering device auth flow", mode)
			return s.runDeviceAuthFlow(ctx, hostname)
		}
		probeCtx, cancel := context.WithTimeout(ctx, tokenValidationTimeout)
		defer cancel()
		if err := s.serverClient.heartbeat(probeCtx, inventory); err != nil {
			if isHTTPStatus(err, http.StatusUnauthorized, http.StatusNotFound) {
				log.Printf("bind mode=%s: stored machine token rejected (%v), entering device auth flow", mode, err)
				s.clearMachineToken()
				return s.runDeviceAuthFlow(ctx, hostname)
			}
			log.Printf("bind mode=%s: token probe skipped due to transient error (%v), keeping existing token", mode, err)
			return nil
		}
		log.Printf("bind mode=%s: existing machine token validated, skipping device auth flow", mode)
		return nil
	default:
		return fmt.Errorf("unsupported bind mode: %s", mode)
	}
}

func (s *Service) runDeviceAuthFlow(ctx context.Context, hostname string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		deviceCode, err := s.serverClient.requestDeviceCode(ctx, hostname)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("failed to request device PIN: %v, retrying in 5s...", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		deviceCode.MachineID = strings.TrimSpace(deviceCode.MachineID)
		deviceCode.Code = strings.TrimSpace(deviceCode.Code)
		if deviceCode.MachineID == "" || deviceCode.Code == "" {
			log.Printf("failed to request device PIN: missing machineId/code in response, retrying in 5s...")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if s.cfg.MachineID != deviceCode.MachineID || s.serverClient.MachineID != deviceCode.MachineID {
			s.mu.Lock()
			s.cfg.MachineID = deviceCode.MachineID
			s.serverClient.MachineID = deviceCode.MachineID
			if err := s.saveConfigRuntimeSafeLocked(); err != nil {
				log.Printf("failed to save config: %v", err)
			}
			s.mu.Unlock()
		}

		fmt.Println()
		fmt.Println("  ┌────────────────────────────────────────────────────────┐")
		fmt.Println("  │  CodeWithPhone Authorization Required                  │")
		fmt.Println("  │                                                        │")
		fmt.Printf("  │  PIN CODE:      %-38s │\n", deviceCode.Code)
		fmt.Println("  │                                                        │")
		fmt.Println("  │  Please enter this PIN on your CodeWithPhone Web/App.  │")
		fmt.Println("  │  Waiting for your confirmation...                      │")
		fmt.Println("  └────────────────────────────────────────────────────────┘")
		fmt.Println()

		// Poll for user entry (max 5 min for one PIN).
		expired := false
		ticker := time.NewTicker(2 * time.Second)
		expiry := time.Now().Add(5 * time.Minute)
		lastStatus := ""

		for !expired {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return ctx.Err()
			case <-ticker.C:
				if time.Now().After(expiry) {
					expired = true
					ticker.Stop()
					break
				}
				res, err := s.serverClient.pollBindingStatus(ctx)
				if err != nil {
					if isHTTPStatus(err, http.StatusNotFound) {
						log.Printf("poll binding status: current PIN expired or missing, refreshing PIN")
						expired = true
						ticker.Stop()
						break
					}
					log.Printf("poll binding status (machineId=%s, baseURL=%s): %v", s.serverClient.MachineID, s.serverClient.BaseURL, err)
					continue
				}
				// Only log when status actually changes — avoids flooding
				// the terminal with "status=pending" every 2 seconds.
				if res.Status != lastStatus {
					log.Printf("poll binding status: status=%s", res.Status)
					lastStatus = res.Status
				}
				if res.Status == "user_entered" {
					ticker.Stop()
					log.Printf("binding request received; waiting for local approval")

					approved := true
					if s.interactive {
						if shouldAutoApproveBindingForTests() {
							approved = true
							log.Printf("binding local approval auto-approved by test mode")
						} else {
							fmt.Printf("\n  Binding request from %s (%s)\n", res.UserName, res.UserEmail)
							fmt.Printf("  Approve? [Y/n]: ")

							// Drain any stale bytes in stdin (leftover
							// newlines from prior prompts or terminal
							// control sequences) before reading the
							// user's actual answer.
							drainStdin()

							scanner := bufio.NewScanner(os.Stdin)
							if scanner.Scan() {
								raw := scanner.Text()
								decision, ok := parseBindingApprovalInput(raw)
								if !ok {
									approved = false
									log.Printf("binding local approval: unrecognized input %q; defaulting to reject", raw)
								} else {
									approved = decision
								}
							} else {
								approved = false
								scanErr := scanner.Err()
								if scanErr != nil {
									log.Printf("binding local approval: input error: %v; defaulting to reject", scanErr)
								} else {
									log.Printf("binding local approval: EOF on stdin; defaulting to reject")
								}
							}
						}
					}

					token, err := s.serverClient.confirmBinding(ctx, res.Code, res.ConfirmNonce, approved)
					if err != nil {
						log.Printf("failed to confirm binding: %v", err)
						expired = true // Retry with new PIN.
						break
					}

					if !approved {
						fmt.Println("  Rejected. Generating new PIN...")
						expired = true
						break
					}

					s.mu.Lock()
					s.cfg.MachineToken = token
					s.serverClient.MachineToken = token
					// Never persist force/token_only from runtime flow to avoid
					// accidental forced rebind on next restart.
					s.cfg.BindMode = config.BindModeAuto
					if err := s.saveConfigRuntimeSafeLocked(); err != nil {
						log.Printf("failed to save config: %v", err)
					}
					s.mu.Unlock()
					log.Printf("device authorized successfully")
					fmt.Println("  Device authorized successfully!")
					return nil
				}
			}
		}
		if expired {
			fmt.Println("  PIN expired. Generating new PIN...")
			fmt.Println()
			continue
		}
	}
}

func (s *Service) clearMachineToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.MachineToken = ""
	s.serverClient.MachineToken = ""
}

func (s *Service) saveConfigRuntimeSafeLocked() error {
	cfgToSave := s.cfg
	if cfgToSave.BindMode == config.BindModeForce || cfgToSave.BindMode == config.BindModeTokenOnly {
		cfgToSave.BindMode = config.BindModeAuto
	}
	return cfgToSave.Save()
}

func (s *Service) persistBindModeAuto() {
	s.mu.Lock()
	if s.cfg.BindMode == config.BindModeAuto {
		s.mu.Unlock()
		return
	}
	s.cfg.BindMode = config.BindModeAuto
	err := s.saveConfigRuntimeSafeLocked()
	s.mu.Unlock()
	if err != nil {
		log.Printf("failed to persist bind_mode=auto: %v", err)
	}
}

func (s *Service) notifyMachineOfflineWithToken(token string) {
	if strings.TrimSpace(s.serverClient.MachineID) == "" || strings.TrimSpace(token) == "" {
		return
	}
	offlineCtx, cancel := context.WithTimeout(context.Background(), machineOfflineNotifyTimeout)
	defer cancel()
	client := s.serverClient
	client.MachineToken = token
	if err := client.markMachineOffline(offlineCtx); err != nil {
		log.Printf("notify machine offline failed: %v", err)
	}
}

func isHTTPStatus(err error, statuses ...int) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	for _, status := range statuses {
		if statusErr.StatusCode == status {
			return true
		}
	}
	return false
}

func parseBindingApprovalInput(input string) (bool, bool) {
	// Strip any non-printable / control characters that terminals sometimes
	// inject (e.g. \x02 STX from copy-paste, arrow key escape sequences).
	// We only care about ASCII letters.
	var cleaned []byte
	for _, b := range []byte(input) {
		if b >= 0x20 && b <= 0x7E {
			cleaned = append(cleaned, b)
		}
	}
	value := strings.ToLower(strings.TrimSpace(string(cleaned)))
	switch value {
	case "", "y", "yes":
		// Empty input (just Enter) defaults to approve ([Y/n] convention).
		return true, true
	case "n", "no":
		return false, true
	default:
		// Last resort: if the cleaned string contains 'y' anywhere,
		// treat as yes. This handles "sy", "2y", etc. from terminal
		// garbage prepended to the user's actual keystroke.
		if strings.Contains(value, "y") && !strings.Contains(value, "n") {
			return true, true
		}
		return false, false
	}
}

// drainStdin discards any bytes already buffered on stdin so that the
// next scanner.Scan() blocks until the user types fresh input. Without
// this, stale newlines from prior prompts or terminal control sequences
// would be consumed immediately, causing the approval prompt to
// auto-reject with an empty string.
func drainStdin() {
	// Set stdin to non-blocking temporarily to read & discard buffered bytes.
	fd := int(os.Stdin.Fd())
	// Use select/poll with zero timeout to check if data is available.
	// Simpler approach: just set a very short read deadline isn't possible
	// on os.Stdin (not a net.Conn). Instead, we use syscall.
	var buf [256]byte
	for {
		// Use SetNonblock to read without waiting.
		if err := syscall.SetNonblock(fd, true); err != nil {
			break
		}
		n, _ := os.Stdin.Read(buf[:])
		_ = syscall.SetNonblock(fd, false)
		if n <= 0 {
			break
		}
	}
}

func shouldAutoApproveBindingForTests() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("DAEMON_TEST_AUTO_APPROVE_BINDING")))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
