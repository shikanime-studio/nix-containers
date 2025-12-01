package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func imageFromStreamLayeredImage(ctx context.Context, path string) (v1.Image, error) {
	tag, err := name.NewTag(getImage().String())
	if err != nil {
		return nil, err
	}
	s, err := streamLayeredImageCommand(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	go func() { _ = s.Run() }()
	slog.Info("streaming layered image", "image", tag)
	return tarball.Image(streamLayeredImageOpener(s), &tag)
}

type streamLayeredImageCmd struct {
	rc  io.ReadCloser
	cmd *exec.Cmd
}

func streamLayeredImageCommand(ctx context.Context, path string) (*streamLayeredImageCmd, error) {
	cmd := exec.CommandContext(ctx, path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	return &streamLayeredImageCmd{rc: stdout, cmd: cmd}, nil
}

func (s *streamLayeredImageCmd) Read(p []byte) (int, error) {
	return s.rc.Read(p)
}

func (s *streamLayeredImageCmd) Close() error {
	if err := s.rc.Close(); err != nil {
		return fmt.Errorf("failed to close read closer: %w", err)
	}
	if err := s.cmd.Wait(); err != nil {
		return fmt.Errorf("failed to wait for command: %w", err)
	}
	return nil
}

func (s *streamLayeredImageCmd) Run() error {
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			slog.Debug(line, "cmd", s.cmd.Path)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("stderr scan failed", "cmd", s.cmd.Path, "err", err)
	}
	return nil
}

func streamLayeredImageOpener(cmd *streamLayeredImageCmd) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return cmd, nil
	}
}

type layeredImageOption func(*layeredImageOptions)

type layeredImageOptions struct {
	acceptFlakeConfig bool
}

func WithAcceptFlakeConfig() layeredImageOption {
	return func(o *layeredImageOptions) { o.acceptFlakeConfig = true }
}

type StreamLayeredImageBuildResult struct {
	DrvPath   string            `json:"drvPath"`
	Outputs   map[string]string `json:"outputs"`
	StartTime int64             `json:"startTime"`
	StopTime  int64             `json:"stopTime"`
}

func buildStreamLayeredImage(
	ctx context.Context,
	url string,
	opts ...layeredImageOption,
) (string, error) {
	o := &layeredImageOptions{}
	for _, opt := range opts {
		opt(o)
	}

	args := []string{"build"}
	if o.acceptFlakeConfig {
		args = append(args, "--accept-flake-config")
	}
	args = append(args, "--json", url)
	cmd := exec.CommandContext(ctx, "nix", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err = cmd.Start(); err != nil {
		slog.Warn("nix build start failed", "url", url, "err", err)
		return "", fmt.Errorf("failed to start command: %w", err)
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				slog.Debug(line, "url", url)
			}
		}
		if err = scanner.Err(); err != nil {
			slog.Warn("stderr scan failed", "url", url, "err", err)
		}
	}()

	outBytes, err := io.ReadAll(stdout)
	if err != nil {
		return "", fmt.Errorf("failed to read stdout: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("failed to wait for command: %w", err)
	}
	var result []*StreamLayeredImageBuildResult
	if err := json.Unmarshal(outBytes, &result); err != nil {
		return "", fmt.Errorf("failed to parse nix build output: %w", err)
	}
	if len(result) == 0 {
		return "", fmt.Errorf("no output path found in nix build result")
	}
	slog.Debug(
		"nix build completed",
		"url", url,
		"drvPath", result[0].DrvPath,
		"out", result[0].Outputs["out"],
	)
	return result[0].Outputs["out"], nil
}
