package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"go.uber.org/zap"
)

// sandboxTimeout は1回のスキャナー実行に許す最大時間
const sandboxTimeout = 30 * time.Minute

// sandboxTmpfsOptions はサンドボックスの /tmp に使う tmpfs のオプション（実行不可・setuid 無効・512 MiB 上限）
const sandboxTmpfsOptions = "rw,noexec,nosuid,nodev,size=512m"

type DockerSandbox struct {
	cli    *client.Client
	logger *zap.SugaredLogger
}

func NewDockerSandbox(logger *zap.SugaredLogger) (*DockerSandbox, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerSandbox{
		cli:    cli,
		logger: logger,
	}, nil
}

func (s *DockerSandbox) RunInSandbox(ctx context.Context, jobID string, imageName string, targetPath string, command []string) (string, string, error) {
	// スキャナー種別を判定（例: clamav, yara）
	scannerName := "sandbox"
	if strings.Contains(imageName, "clamav") {
		scannerName = "clamav"
	} else if strings.Contains(imageName, "yara") {
		scannerName = "yara"
	}

	// 識別しやすいコンテナ名を構築（例: sfsp-sandbox-clamav-cde771ac-0607-47f0-8004-66295d7b2793）
	containerName := fmt.Sprintf("sfsp-sandbox-%s-%s", scannerName, jobID)

	s.logger.Infof("Running command in sandbox container [%s]: image=%s, command=%v", containerName, imageName, command)

	// イメージの存在確認。スキャナーのイメージは compose でローカルにビルドするもの
	// （sfsp-clamav-client / sfsp-yara-client）なので pull はしない。
	// これにより Docker API で必要なのは images/{name}/json（GET）だけになる（docker-socket-proxy で POST /images/create を許可しなくてよい）
	if _, err := s.cli.ImageInspect(ctx, imageName); err != nil {
		return "", "", fmt.Errorf("scanner image %s is not available locally (build it with docker compose build): %w", imageName, err)
	}

	// ベースディレクトリ (/tmp/sfsp-scan) からの相対パスを安全に取得
	const baseDir = "/tmp/sfsp-scan"
	cleanBase := filepath.Clean(baseDir)
	cleanTarget := filepath.Clean(targetPath)

	var relPath string
	if strings.HasPrefix(cleanTarget, cleanBase) {
		var relErr error
		relPath, relErr = filepath.Rel(cleanBase, cleanTarget)
		if relErr != nil {
			relPath = strings.TrimPrefix(cleanTarget, cleanBase)
		}
	} else {
		relPath = cleanTarget
	}

	// 先頭のスラッシュを除去してマウント先パスに結合
	relPath = strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	containerFilePath := filepath.ToSlash(filepath.Join("/scan_target", relPath))

	s.logger.Infof("Mapped targetPath [%s] -> containerFilePath [%s]", targetPath, containerFilePath)

	// コマンド内の {FILE_PATH} を置換
	formattedCmd := make([]string, len(command))
	for i, c := range command {
		formattedCmd[i] = strings.ReplaceAll(c, "{FILE_PATH}", containerFilePath)
	}

	// コンテナ設定（JobID や Scanner 情報のラベルを追加）
	// スキャナーを直接 ENTRYPOINT として実行する。イメージ側の初期化スクリプト（clamav/clamav の /init 等）は
	// chown などを行うため、CapDrop: ALL の下では失敗し、スキャン前に非 0 終了してしまう
	config := &container.Config{
		Image:      imageName,
		Entrypoint: formattedCmd,
		Tty:        false,
		WorkingDir: "/scan_target",
		Labels: map[string]string{
			"app":     "sfsp-sandbox",
			"job_id":  jobID,
			"scanner": scannerName,
		},
	}

	// 環境変数からボリューム名を取得（デフォルト: sfsp_temp_scan）
	volumeName := os.Getenv("SFSP_SCAN_VOLUME_NAME")
	if volumeName == "" {
		volumeName = "sfsp_temp_scan"
	}

	// Binds設定 (AutoRemoveはfalseにし、明示的にdeferで削除する)
	// スキャン対象は信頼できないファイルのため、ネットワーク遮断・権限削減・リソース制限を行う
	pidsLimit := int64(256)
	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/scan_target:ro", volumeName),
		},
		AutoRemove:  false,
		NetworkMode: "none",
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		// ルートファイルシステムは読み取り専用にし、スキャナーが一時ファイルを書く /tmp だけ
		// サイズ上限付きの tmpfs として与える。tmpfs の使用量は下の Memory 上限にも含まれる。
		// ClamAV はアーカイブを展開するときに /tmp を使う。上限を超えるとスキャンはエラー終了
		// （判定不能 = failed）になり、clean にはならない
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			"/tmp": sandboxTmpfsOptions,
		},
		Resources: container.Resources{
			Memory:    1536 * 1024 * 1024,
			NanoCPUs:  1_000_000_000,
			PidsLimit: &pidsLimit,
		},
	}

	// ハングしたスキャンでワーカーが永久に停止しないようタイムアウトを設ける
	ctx, cancel := context.WithTimeout(ctx, sandboxTimeout)
	defer cancel()

	// 前回異常終了等で同名コンテナが残っていた場合は事前に削除
	_ = s.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})

	resp, err := s.cli.ContainerCreate(ctx, config, hostConfig, nil, nil, containerName)
	if err != nil {
		return "", "-1", fmt.Errorf("failed to create container %s: %w", containerName, err)
	}

	// 処理終了時に確実にコンテナを削除する
	defer func() {
		_ = s.cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := s.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", "-1", fmt.Errorf("failed to start container %s: %w", containerName, err)
	}

	// コンテナの終了を待機
	statusCh, errCh := s.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)

	select {
	case err := <-errCh:
		if err != nil {
			return "", "-1", fmt.Errorf("error waiting for container %s: %w", containerName, err)
		}
	case status := <-statusCh:
		// コンテナ停止後にログを取得 (AutoRemove: false のため安全に取得可能)
		out, err := s.cli.ContainerLogs(ctx, resp.ID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
		})
		if err != nil {
			return "", "-1", fmt.Errorf("failed to get container logs for %s: %w", containerName, err)
		}
		defer out.Close()

		var stdoutBuf, stderrBuf bytes.Buffer
		_, err = stdcopy.StdCopy(&stdoutBuf, &stderrBuf, out)
		if err != nil {
			s.logger.Warnf("Failed to demultiplex logs: %v", err)
		}

		s.logger.Infof("Sandbox finished: container=%s image=%s exitCode=%d", containerName, imageName, status.StatusCode)
		if status.Error != nil {
			return stdoutBuf.String(), stderrBuf.String(), fmt.Errorf("container %s wait error: %s", containerName, status.Error.Message)
		}
		if status.StatusCode != 0 {
			// 終了コードは呼び出し側（スキャナー）が判定に使うため ExitError として返す
			return stdoutBuf.String(), stderrBuf.String(), &ExitError{Code: status.StatusCode}
		}
		return stdoutBuf.String(), stderrBuf.String(), nil
	}

	return "", "-1", fmt.Errorf("unexpected end of container execution for %s", containerName)
}
