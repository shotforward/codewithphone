package app

import (
	"log"
	"os"
	"strings"
)

func debugLogsEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("CODEWITHPHONE_DEBUG_LOGS")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func debugLogf(format string, args ...any) {
	if !debugLogsEnabled() {
		return
	}
	log.Printf(format, args...)
}
