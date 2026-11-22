//go:build unix

package cli

import (
	"os/signal"
	"syscall"
)

func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}
