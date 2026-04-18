package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"golang.org/x/sync/errgroup"
)

// BuilderType indicates the type of a Nix flake package.
type BuilderType int

const (
	// UnknownBuilderType indicates the package type is unknown.
	UnknownBuilderType BuilderType = iota
	// StreamBuilderType indicates a streamable image package.
	StreamBuilderType
	// TarGzBuilderType indicates a tar.gz package.
	TarGzBuilderType
)

type flakeShowPackage struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type flakeShowOutput struct {
	Packages map[string]map[string]flakeShowPackage `json:"packages"`
}

func getImageBuilderType(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	p *v1.Platform,
	opts ...imageOption,
) (BuilderType, error) {
	o := makeImageOptions(opts...)

	args := []string{"flake", "show", "--json", "--all-systems", buildContext}
	if o.noPureEval {
		args = append(args, "--no-pure-eval")
	}
	cmd := exec.CommandContext(ctx, "nix", args...)
	slog.DebugContext(ctx, "checking image builder type", "cmd", cmd.Path, "args", args)

	output, err := cmd.Output()
	if err != nil {
		return UnknownBuilderType, fmt.Errorf("failed to run nix flake show: %w", err)
	}

	var showOutput flakeShowOutput
	if err := json.Unmarshal(output, &showOutput); err != nil {
		return UnknownBuilderType, fmt.Errorf("failed to parse nix flake show output: %w", err)
	}

	system := formatSystemName(p)
	pkgName := formatNixFlakePackageName(ref)

	pkgs, ok := showOutput.Packages[system]
	if !ok {
		return UnknownBuilderType, fmt.Errorf("system %s not found in flake output", system)
	}

	pkg, ok := pkgs[pkgName]
	if !ok {
		return UnknownBuilderType, fmt.Errorf("package %s not found for system %s", pkgName, system)
	}

	if strings.HasPrefix(pkg.Name, "stream-") {
		slog.InfoContext(
			ctx,
			"resolved builder type",
			"ref",
			ref.Name(),
			"system",
			system,
			"package",
			pkgName,
			"builder_type",
			StreamBuilderType,
			"artifact_name",
			pkg.Name,
		)
		return StreamBuilderType, nil
	}
	if strings.HasSuffix(pkg.Name, ".tar.gz") {
		slog.InfoContext(
			ctx,
			"resolved builder type",
			"ref",
			ref.Name(),
			"system",
			system,
			"package",
			pkgName,
			"builder_type",
			TarGzBuilderType,
			"artifact_name",
			pkg.Name,
		)
		return TarGzBuilderType, nil
	}

	slog.WarnContext(
		ctx,
		"resolved builder type",
		"ref",
		ref.Name(),
		"system",
		system,
		"package",
		pkgName,
		"builder_type",
		UnknownBuilderType,
		"artifact_name",
		pkg.Name,
	)
	return UnknownBuilderType, nil
}

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

func loadImage(
	ctx context.Context,
	ref name.Reference,
	path string,
) (name.Reference, error) {
	slog.InfoContext(ctx, "load image", "image", ref, "path", path)
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("create docker client failed: %w", err)
	}
	cli.NegotiateAPIVersion(ctx)

	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open image: %w", err)
	}
	defer func() { _ = input.Close() }()
	resp, err := cli.ImageLoad(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("docker image load failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	loadedRef, err := readImageLoadedRef(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("failed to read loaded ref: %w", err)
	}

	return loadedRef, nil
}

func loadStreamImage(
	ctx context.Context,
	ref name.Reference,
	path string,
) (name.Reference, error) {
	slog.InfoContext(ctx, "start stream image command", "image", ref, "path", path)
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

	slog.InfoContext(ctx, "streaming image", "image", ref)
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("create docker client failed: %w", err)
	}
	cli.NegotiateAPIVersion(ctx)

	resp, err := cli.ImageLoad(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("docker image load failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
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

	slog.InfoContext(ctx, "stream image command completed", "image", ref, "path", path)
	return loadedRef, nil
}

type imageOption func(*imageOptions)

type imageOptions struct {
	acceptFlakeConfig bool
	noPureEval        bool
}

func WithAcceptFlakeConfig() imageOption {
	return func(o *imageOptions) { o.acceptFlakeConfig = true }
}

func WithNoPureEval() imageOption {
	return func(o *imageOptions) { o.noPureEval = true }
}

func makeImageOptions(opts ...imageOption) *imageOptions {
	o := &imageOptions{
		acceptFlakeConfig: true,
		noPureEval:        true,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

type buildImageBuildResult struct {
	DrvPath   string            `json:"drvPath"`
	Outputs   map[string]string `json:"outputs"`
	StartTime int64             `json:"startTime"`
	StopTime  int64             `json:"stopTime"`
}

func buildImage(
	ctx context.Context,
	url string,
	opts ...imageOption,
) (string, error) {
	o := makeImageOptions(opts...)

	args := []string{"build"}
	if o.acceptFlakeConfig {
		args = append(args, "--accept-flake-config", "--no-link")
	}
	args = append(args, "--json", url)
	cmd := exec.CommandContext(ctx, "nix", args...)
	slog.InfoContext(ctx, "start nix build", "url", url, "args", args)

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

	var result []*buildImageBuildResult
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
	slog.InfoContext(
		ctx,
		"nix build completed",
		"url",
		url,
		"drv_path",
		result[0].DrvPath,
		"out",
		result[0].Outputs["out"],
	)
	return result[0].Outputs["out"], nil
}
