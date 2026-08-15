package sandbox

import "context"

type Sandbox interface {
RunInSandbox(ctx context.Context, imageName string, command []string, targetPath string) (string, int, error)
}
