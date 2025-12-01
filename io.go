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

	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/name"
	"golang.org/x/sync/errgroup"
)

type imageLoadProgress struct {
	Status         string         `json:"status"`
	Progress       string         `json:"progress"`
	ID             string         `json:"id"`
	ProgressDetail map[string]any `json:"progressDetail"`
}

type imageLoadResult struct {
	Stream string `json:"stream"`
}

func readImageLoadedRef(
	ctx context.Context,
	r *bufio.Reader,
) (name.Reference, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read line: %w", err)
		}
		var progress imageLoadProgress
		if err = json.Unmarshal([]byte(line), &progress); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to decode image load progress: %w", err)
		}
		if progress.Status == "Loading layer" {
			slog.DebugContext(
				ctx,
				"loading layer",
				"id",
				progress.ID,
				"progress",
				progress.Progress,
			)
		} else {
			var result imageLoadResult
			if err = json.Unmarshal([]byte(line), &result); err != nil {
				return nil, fmt.Errorf("failed to decode image load result: %w", err)
			}
			slog.DebugContext(ctx, "loaded image", "stream", result.Stream)
			loadedRef, err := name.ParseReference(
				strings.TrimSpace(strings.TrimPrefix(result.Stream, "Loaded image: ")),
			)
			if err != nil {
				return nil, err
			}
			return loadedRef, nil
		}
	}
	return nil, fmt.Errorf("failed to read loaded ref")
}

func loadStreamLayeredImage(
	ctx context.Context,
	ref name.Reference,
	path string,
) (name.Reference, error) {
	cmd := exec.CommandContext(ctx, path)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stream := bufio.NewReader(stdoutPipe)

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	sc := bufio.NewScanner(stderrPipe)

	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start stream command: %w", err)
	}

	wg := errgroup.Group{}
	wg.Go(func() error {
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				slog.DebugContext(ctx, line, "cmd", cmd.Path)
			}
		}
		if err = sc.Err(); err != nil {
			return fmt.Errorf("stderr scan failed: %w", err)
		}
		return nil
	})

	slog.InfoContext(ctx, "streaming layered image", "image", ref)
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("create docker client failed: %w", err)
	}
	cli.NegotiateAPIVersion(ctx)

	resp, err := cli.ImageLoad(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("docker image load failed: %w", err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)

	loadedRef, err := readImageLoadedRef(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("failed to read loaded ref: %w", err)
	}

	if err = wg.Wait(); err != nil {
		return nil, fmt.Errorf("failed to wait for stream command: %w", err)
	}
	if err = cmd.Wait(); err != nil {
		return nil, fmt.Errorf("failed to wait for command: %w", err)
	}

	return loadedRef, nil
}

type layeredImageOption func(*layeredImageOptions)

type layeredImageOptions struct {
	acceptFlakeConfig bool
}

func WithAcceptFlakeConfig() layeredImageOption {
	return func(o *layeredImageOptions) { o.acceptFlakeConfig = true }
}

func makeLayeredImageOptions(opts ...layeredImageOption) *layeredImageOptions {
	o := &layeredImageOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

type streamLayeredImageBuildResult struct {
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
	o := makeLayeredImageOptions(opts...)

	args := []string{"build"}
	if o.acceptFlakeConfig {
		args = append(args, "--accept-flake-config")
	}
	args = append(args, "--json", url)
	cmd := exec.CommandContext(ctx, "nix", args...)
	slog.DebugContext(ctx, "running command", "cmd", cmd.Path, "args", args)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	dec := json.NewDecoder(bufio.NewReader(stdoutPipe))

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	sc := bufio.NewScanner(stderrPipe)

	if err = cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to run command: %w", err)
	}

	wg := errgroup.Group{}
	wg.Go(func() error {
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				slog.DebugContext(ctx, line, "url", url)
			}
		}
		if err = sc.Err(); err != nil {
			return fmt.Errorf("stderr scan failed: %w", err)
		}
		return nil
	})

	var result []*streamLayeredImageBuildResult
	if err := dec.Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse nix build output: %w", err)
	}

	if err := wg.Wait(); err != nil {
		return "", fmt.Errorf("failed to wait for command: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("failed to wait for command: %w", err)
	}

	if len(result) == 0 {
		return "", fmt.Errorf("no output path found in nix build result")
	}
	slog.DebugContext(ctx,
		"nix build completed",
		"url", url,
		"drvPath", result[0].DrvPath,
		"out", result[0].Outputs["out"],
	)
	return result[0].Outputs["out"], nil
}
