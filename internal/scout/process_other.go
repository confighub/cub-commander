//go:build !unix

package scout

import "os/exec"

// Non-release platforms retain CommandContext's direct-process cancellation.
func configureProcess(cmd *exec.Cmd) {}
