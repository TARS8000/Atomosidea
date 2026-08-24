package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings" // Added missing import
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"go.uber.org/zap"
)

// DockerSandbox provides an execution environment for scanners within a Docker container
type DockerSandbox struct {
	cli    *client.Client
	logger *zap.SugaredLogger
}

// NewDockerSandbox creates a new DockerSandbox instance
func NewDockerSandbox(logger *zap.SugaredLogger) (*DockerSandbox, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &DockerSandbox{cli: cli, logger: logger}, nil
}

// RunInSandbox runs a command inside a new, isolated Docker container
func (s *DockerSandbox) RunInSandbox(ctx context.Context, image, filePathToScan string, command []string) (string, string, error) {
	s.logger.Infof("Running command in sandbox: image=%s, command=%v", image, command)

	// Pull the image if it's not available locally
	reader, err := s.cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		s.logger.Warnf("Failed to pull image %s, will try to use local: %v", image, err)
	} else {
		io.Copy(os.Stdout, reader)
		reader.Close()
	}

	absFilePath, err := filepath.Abs(filePathToScan)
	if err != nil {
		return "", "", fmt.Errorf("failed to get absolute path for %s: %w", filePathToScan, err)
	}
	hostPath := filepath.Dir(absFilePath)
	containerPath := "/scan_target"
	targetFileInContainer := filepath.Join(containerPath, filepath.Base(absFilePath))

	for i, arg := range command {
		if arg == "{FILE_PATH}" {
			command[i] = targetFileInContainer
		}
	}

	resp, err := s.cli.ContainerCreate(ctx, &container.Config{
		Image:        image,
		Cmd:          command,
		Tty:          false,
		AttachStdout: true,
		AttachStderr: true,
	}, &container.HostConfig{
		NetworkMode: "none",
		Resources: container.Resources{
			Memory: 512 * 1024 * 1024, // 512MB
			CPUs:   1,
		},
		ReadonlyRootfs: true,
		Binds: []string{
			fmt.Sprintf("%s:%s:ro", hostPath, containerPath),
			fmt.Sprintf("%s:%s:ro", "/path/to/yara-rules", "/etc/yara-rules"), // This needs to be configured
		},
	}, nil, nil, "")
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	defer s.cli.ContainerRemove(ctx, resp.ID, types.ContainerRemoveOptions{Force: true})

	if err := s.cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	statusCh, errCh := s.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return "", "", fmt.Errorf("error waiting for container: %w", err)
		}
	case <-statusCh:
		// Container finished
	case <-ctx.Done():
		return "", "", ctx.Err()
	}

	out, err := s.cli.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", "", fmt.Errorf("failed to get container logs: %w", err)
	}
	defer out.Close()

	stdOut, stdErr := new(strings.Builder), new(strings.Builder)
	_, err = stdcopy.StdCopy(stdOut, stdErr, out)
	if err != nil {
		return "", "", fmt.Errorf("failed to demultiplex logs: %w", err)
	}

	return stdOut.String(), stdErr.String(), nil
}
