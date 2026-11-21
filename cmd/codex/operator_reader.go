package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pairtask/pairtask/internal/host"
)

// Some inherited stdin descriptors cannot interrupt a blocking Read on Close.
// Do not let that descriptor keep a stopped App Server alive. The launcher owns
// one process lifetime; a blocked reader may survive until process exit. Check
// cancellation before dispatch and pass that same context through the handler.
func readOperatorFrames(ctx context.Context, input io.Reader, dispatch func(host.Frame) error) error {
	result := make(chan error, 1)
	go func() {
		result <- host.ReadFrames(input, func(frame host.Frame) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return dispatch(frame)
		})
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		if errors.Is(err, host.ErrFrameTooLarge) {
			return fmt.Errorf("from the operator client: %w", err)
		}
		return err
	}
}
