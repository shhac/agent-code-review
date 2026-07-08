package scheduler

import (
	"fmt"
	"os"
	"time"
)

func newRunID() string { return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid()) }

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
