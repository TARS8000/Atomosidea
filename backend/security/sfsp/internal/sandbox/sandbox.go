package sandbox

import (
	"context"
	"errors"
	"fmt"
)

// ExitError is returned by RunInSandbox when the command exits with a non-zero status.
// stdout/stderr are still returned alongside it.
type ExitError struct {
	Code int64
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("sandbox command exited with status %d", e.Code)
}

// ExitCode returns the exit code carried by err, or -1 if err is not an ExitError.
func ExitCode(err error) int64 {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return -1
}

// Sandbox defines the interface for executing commands in an isolated environment.
type Sandbox interface {
	RunInSandbox(ctx context.Context, jobID string, image string, filePathToScan string, command []string) (stdout string, stderr string, err error)
}

