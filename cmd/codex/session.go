package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pairtask/pairtask/internal/host"
)

func (l codexLauncher) beginSession() (func(bool) error, error) {
	if !l.restricted {
		return func(bool) error { return nil }, nil
	}
	if l.budget == nil {
		return nil, errors.New("restricted operator requires a budget")
	}
	if err := l.budget.BeginSession(); err != nil {
		return nil, err
	}
	return l.budget.EndSession, nil
}

func readHostFrames(ctx context.Context, p *host.Proxy, output io.Reader) error {
	err := host.ReadFrames(output, func(f host.Frame) error { return p.FromHost(ctx, f) })
	if errors.Is(err, host.ErrFrameTooLarge) {
		return fmt.Errorf("from the Codex App Server: %w", err)
	}
	if err != io.EOF {
		return err
	}
	if p.RestrictedOperator && ctx.Err() == nil {
		return errors.New("restricted native client disconnected unexpectedly; accounting is unconfirmed")
	}
	return err
}
