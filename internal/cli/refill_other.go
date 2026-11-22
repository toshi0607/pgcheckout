//go:build !unix

package cli

import (
	"errors"
	"os/exec"
)

func detach(*exec.Cmd) error {
	return errors.New("detached pool refill is unavailable on this platform")
}
