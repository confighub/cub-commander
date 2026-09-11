package scout

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const maxOutput = 2 << 20

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.overflow = true
		b.cancel()
		return 0, errors.New("Scout output limit exceeded")
	}
	return b.buffer.Write(p)
}

func run(ctx context.Context, binary string, args []string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	configureProcess(cmd)
	cmd.WaitDelay = time.Second
	stdout := &cappedBuffer{limit: maxOutput, cancel: cancel}
	stderr := &cappedBuffer{limit: 64 << 10, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run() // Wait reaps the process before returning on all exit paths.
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("Scout output limit exceeded; evidence unavailable")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("Scout read cancelled or timed out")
		}
		// stderr can contain credentials from an auth helper. Never put it in the UI.
		return nil, errors.New("Scout could not complete the bounded read; check the executable, provider version and kube-context credentials")
	}
	return stdout.buffer.Bytes(), nil
}
