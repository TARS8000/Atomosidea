package sandbox

import "context"

// Sandbox defines the interface for executing commands in an isolated environment.
type Sandbox interface {
	RunInSandbox(ctx context.Context, jobID string, image string, filePathToScan string, command []string) (stdout string, stderr string, err error)
}

