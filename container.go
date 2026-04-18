package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/errgroup"
)

var newDockerClient = func(ctx context.Context) (*client.Client, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, err
	}
	docker.NegotiateAPIVersion(ctx)
	return docker, nil
}

var streamCommandContext = exec.CommandContext

type ContainerOption func(*containerOptions)

type containerOptions struct {
	keychain  authn.Keychain
	transport http.RoundTripper
	remote    []remote.Option
}

type ContainerClient struct {
	docker    *client.Client
	keychain  authn.Keychain
	transport http.RoundTripper
	remote    []remote.Option
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

func WithContainerKeychain(kc authn.Keychain) ContainerOption {
	return func(o *containerOptions) {
		o.keychain = kc
		o.remote = append(o.remote, remote.WithAuthFromKeychain(kc))
	}
}

func WithContainerTransport(t http.RoundTripper) ContainerOption {
	return func(o *containerOptions) {
		o.transport = t
		o.remote = append(o.remote, remote.WithTransport(t))
	}
}

func WithContainerRemoteOption(opt remote.Option) ContainerOption {
	return func(o *containerOptions) {
		o.remote = append(o.remote, opt)
	}
}

func NewContainerClient(ctx context.Context, opts ...ContainerOption) (*ContainerClient, error) {
	o := &containerOptions{
		keychain:  authn.DefaultKeychain,
		transport: http.DefaultTransport,
	}
	o.remote = append(o.remote, remote.WithAuthFromKeychain(o.keychain))
	o.remote = append(o.remote, remote.WithTransport(o.transport))
	for _, opt := range opts {
		opt(o)
	}

	docker, err := newDockerClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create docker client failed: %w", err)
	}

	return &ContainerClient{
		docker:    docker,
		keychain:  o.keychain,
		transport: o.transport,
		remote:    o.remote,
	}, nil
}

func (c *ContainerClient) CheckPushPermission(ref name.Reference) error {
	if err := remote.CheckPushPermission(ref, c.keychain, c.transport); err != nil {
		return fmt.Errorf("check push permission failed: %w", err)
	}
	return nil
}

func (c *ContainerClient) TagImage(
	ctx context.Context,
	loadedRef, ref name.Reference,
) error {
	if err := c.docker.ImageTag(ctx, loadedRef.Name(), ref.Name()); err != nil {
		return fmt.Errorf("tag image failed: %w", err)
	}
	_, err := c.docker.ImageRemove(ctx, loadedRef.Name(), image.RemoveOptions{})
	if err != nil {
		return fmt.Errorf("remove image failed: %w", err)
	}
	return nil
}

func (c *ContainerClient) LoadImage(
	ctx context.Context,
	ref name.Reference,
	path string,
) (name.Reference, error) {
	slog.InfoContext(ctx, "load image", "image", ref, "path", path)

	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open image: %w", err)
	}
	defer func() { _ = input.Close() }()

	resp, err := c.docker.ImageLoad(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("docker image load failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	loadedRef, err := readImageLoadedRef(ctx, bufio.NewReader(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("failed to read loaded ref: %w", err)
	}

	return loadedRef, nil
}

func (c *ContainerClient) LoadStreamImage(
	ctx context.Context,
	ref name.Reference,
	path string,
) (name.Reference, error) {
	slog.InfoContext(ctx, "start stream image command", "image", ref, "path", path)
	cmd := streamCommandContext(ctx, path)

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
	resp, err := c.docker.ImageLoad(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("docker image load failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	loadedRef, err := readImageLoadedRef(ctx, bufio.NewReader(resp.Body))
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

func (c *ContainerClient) PushImage(ref name.Reference) error {
	img, err := daemon.Image(ref)
	if err != nil {
		return fmt.Errorf("load image failed: %w", err)
	}
	if err := remote.Write(ref, img, c.remote...); err != nil {
		return fmt.Errorf("push image failed: %w", err)
	}
	return nil
}

func (c *ContainerClient) PushPlatformImage(
	ref name.Reference,
	p *v1.Platform,
) (mutate.IndexAddendum, error) {
	img, err := daemon.Image(ref)
	if err != nil {
		return mutate.IndexAddendum{}, fmt.Errorf("load image failed: %w", err)
	}
	if err := remote.Write(ref, img, c.remote...); err != nil {
		return mutate.IndexAddendum{}, fmt.Errorf("push image failed: %w", err)
	}
	return mutate.IndexAddendum{
		Add:        img,
		Descriptor: v1.Descriptor{Platform: p},
	}, nil
}

func (c *ContainerClient) PushManifest(
	ref name.Reference,
	adds []mutate.IndexAddendum,
) error {
	if err := remote.WriteIndex(ref, mutate.AppendManifests(empty.Index, adds...), c.remote...); err != nil {
		return fmt.Errorf("push manifest failed: %w", err)
	}
	return nil
}

func (c *ContainerClient) TrackImage(ref name.Reference) error {
	img, err := remote.Image(ref, c.remote...)
	if err != nil {
		return fmt.Errorf("pull manifest failed: %w", err)
	}
	tag, err := name.NewTag(ref.Name())
	if err != nil {
		return fmt.Errorf("failed to format manifest reference: %w", err)
	}
	if _, err := daemon.Write(tag, img); err != nil {
		return fmt.Errorf("failed to write manifest to daemon: %w", err)
	}
	return nil
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
