package scout

import (
	"context"
	"errors"
	"sync"
)

// ProcessSession supervises one-shot subprocess reads. Close cancels and waits
// for every active read to reap its child before the application exits.
// It retains no observation or credential cache.
type ProcessSession struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	reads  sync.WaitGroup
}

func NewProcessSession(config Config) *ProcessSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &ProcessSession{config: config, ctx: ctx, cancel: cancel}
}

func (s *ProcessSession) Load(ctx context.Context, req Request) (Snapshot, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Snapshot{}, errors.New("Scout provider session closed")
	}
	s.reads.Add(1)
	s.mu.Unlock()
	defer s.reads.Done()
	child, cancel := context.WithCancel(s.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if ctx.Err() != nil {
		cancel()
	}
	return s.config.Load(child, req)
}

func (s *ProcessSession) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.reads.Wait()
}
