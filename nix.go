package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"golang.org/x/sync/errgroup"
)

var nixCommandContext = exec.CommandContext

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

type imageOption func(*imageOptions)

type imageOptions struct {
	acceptFlakeConfig bool
	noPureEval        bool
}

type NixClient struct{}

type flakeShowPackage struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type flakeShowOutput struct {
	Packages map[string]map[string]flakeShowPackage `json:"packages"`
}

type buildImageBuildResult struct {
	DrvPath   string            `json:"drvPath"`
	Outputs   map[string]string `json:"outputs"`
	StartTime int64             `json:"startTime"`
	StopTime  int64             `json:"stopTime"`
}

func NewNixClient() *NixClient {
	return &NixClient{}
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

func (n *NixClient) GetImageBuilderType(
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
	cmd := nixCommandContext(ctx, "nix", args...)
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

func (n *NixClient) BuildPlatformImage(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	p *v1.Platform,
	opts ...imageOption,
) (string, error) {
	return n.BuildImage(ctx, formatNixFlakePackage(buildContext, ref, p), opts...)
}

func (n *NixClient) BuildImage(
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
	cmd := nixCommandContext(ctx, "nix", args...)
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
