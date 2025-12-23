package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

type BuildOption func(*buildOption)

type buildOption struct {
	remote  []remote.Option
	layered []layeredImageOption
	push    bool
}

func WithRemoteOption(opt remote.Option) BuildOption {
	return func(o *buildOption) { o.remote = append(o.remote, opt) }
}

func WithStreamLayeredImageOption(opt layeredImageOption) BuildOption {
	return func(o *buildOption) { o.layered = append(o.layered, opt) }
}

func WithPush(push bool) BuildOption {
	return func(o *buildOption) { o.push = push }
}

func makeBuildOption(opts ...BuildOption) *buildOption {
	o := &buildOption{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

var (
	rootCmd = &cobra.Command{
		Use:   "nix-containers",
		Short: "Build OCI images from Nix flakes",
		Long:  "CLI to build and optionally push OCI images produced from Nix flakes. Primarily intended for Skaffold custom builders. Configure via env vars: IMAGE, PLATFORMS, BUILD_CONTEXT, PUSH_IMAGE, LOG_LEVEL, ACCEPT_FLAKE_CONFIG.",
		Example: "# Show help\n" +
			"./nix-containers --help\n\n" +
			"# Build via Skaffold custom builder\n" +
			"IMAGE=ghcr.io/you/app:latest PLATFORMS=linux/amd64 BUILD_CONTEXT=. PUSH_IMAGE=true ./nix-containers skaffold build",
	}

	buildCmd = &cobra.Command{
		Use:   "build [BUILD_CONTEXT]",
		Short: "Build and optionally push images (root variant)",
		Long:  "Builds OCI images from a Nix flake at BUILD_CONTEXT and optionally pushes them. Configure via env vars: IMAGE, PLATFORMS, PUSH_IMAGE, LOG_LEVEL, ACCEPT_FLAKE_CONFIG.",
		Example: "# Build from current directory and push\n" +
			"IMAGE=ghcr.io/you/app:latest PLATFORMS=linux/amd64 PUSH_IMAGE=true ./nix-containers build .",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			image, err := getImage()
			if err != nil {
				return fmt.Errorf("failed to get image: %w", err)
			}
			plats := getPlatforms()
			pushImage := getPushImage()
			acceptFlake := getAcceptFlakeConfig()
			buildContext := ""
			if len(args) > 0 {
				buildContext = args[0]
			} else {
				var err error
				buildContext, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get current working directory: %w", err)
				}
			}
			if buildContext == "" {
				return fmt.Errorf(
					"build context must be provided via arg or --build-context/BUILD_CONTEXT",
				)
			}
			slog.InfoContext(ctx,
				"build config",
				"image", image.String(),
				"platforms", plats,
				"build_context", buildContext,
				"push", pushImage,
				"accept_flake_config", acceptFlake,
			)
			opts := []BuildOption{
				WithPush(pushImage),
				WithRemoteOption(remote.WithAuthFromKeychain(authn.DefaultKeychain)),
			}
			if acceptFlake {
				opts = append(opts, WithStreamLayeredImageOption(WithAcceptFlakeConfig()))
			}
			return buildAndPush(
				ctx,
				buildContext,
				image,
				plats,
				opts...)
		},
	}
)

func init() {
	rootCmd.PersistentFlags().
		Bool("accept-flake-config", false, "accept nix flake config during build")
	if err := viper.BindPFlag("accept_flake_config", rootCmd.PersistentFlags().Lookup("accept-flake-config")); err != nil {
		slog.Error("bind flag failed", "flag", "accept-flake-config", "err", err)
		os.Exit(1)
	}
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().String(
		"image",
		"",
		"destination image reference (e.g., ghcr.io/you/app:tag)",
	)
	if err := viper.BindPFlag("image", buildCmd.Flags().Lookup("image")); err != nil {
		slog.Error("bind flag failed", "flag", "image", "err", err)
		os.Exit(1)
	}
	buildCmd.Flags().String(
		"build-context",
		"",
		"path to the flake build context (defaults to positional arg or BUILD_CONTEXT)",
	)
	if err := viper.BindPFlag("build_context", buildCmd.Flags().Lookup("build-context")); err != nil {
		slog.Error("bind flag failed", "flag", "build-context", "err", err)
		os.Exit(1)
	}
	buildCmd.Flags().Bool(
		"push",
		false,
		"push built images to the registry",
	)
	if err := viper.BindPFlag("push_image", buildCmd.Flags().Lookup("push")); err != nil {
		slog.Error("bind flag failed", "flag", "push", "err", err)
		os.Exit(1)
	}
	buildCmd.Flags().String(
		"platforms",
		"",
		"comma-separated target platforms os/arch (e.g., linux/amd64,linux/arm64)",
	)
	if err := viper.BindPFlag("platforms", buildCmd.Flags().Lookup("platforms")); err != nil {
		slog.Error("bind flag failed", "flag", "platforms", "err", err)
		os.Exit(1)
	}
}

func main() {
	logLevel, err := getLogLevel()
	if err != nil {
		slog.Error("get log level failed", "err", err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(
		os.Stderr,
		&slog.HandlerOptions{Level: logLevel},
	)))
	if err := rootCmd.Execute(); err != nil {
		slog.Error("command failed", "err", err)
		os.Exit(1)
	}
}

func tagImage(ctx context.Context, loadedRef, ref name.Reference) error {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return fmt.Errorf("create docker client failed: %w", err)
	}
	cli.NegotiateAPIVersion(ctx)
	if err = cli.ImageTag(ctx, loadedRef.Name(), ref.Name()); err != nil {
		return fmt.Errorf("tag image failed: %w", err)
	}
	_, err = cli.ImageRemove(ctx, loadedRef.Name(), image.RemoveOptions{})
	if err != nil {
		return fmt.Errorf("remove image failed: %w", err)
	}
	return nil
}

func buildPlatformImage(
	ctx context.Context,
	buildContext string,
	p *v1.Platform,
	ref name.Reference,
	opts ...BuildOption,
) (name.Reference, error) {
	o := makeBuildOption(opts...)
	slog.InfoContext(ctx, "build image", "ref", ref.Name(), "os", p.OS, "arch", p.Architecture)
	path, err := buildStreamLayeredImage(
		ctx,
		formatNixFlakePackage(buildContext, ref, p),
		o.layered...,
	)
	if err != nil {
		return nil, fmt.Errorf("build stream layered image failed: %w", err)
	}
	return loadStreamLayeredImage(ctx, ref, path)
}

func buildAndPushMultiplatformImage(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	ps []*v1.Platform,
	opts ...BuildOption,
) error {
	o := makeBuildOption(opts...)
	if !o.push {
		return fmt.Errorf(
			"multiplatform image build is only supported when pushing to remote registry",
		)
	}
	var adds []mutate.IndexAddendum
	wg, ctx := errgroup.WithContext(ctx)
	for _, p := range ps {
		wg.Go(func() error {
			loadedRef, err := buildPlatformImage(ctx, buildContext, p, ref)
			if err != nil {
				return err
			}
			platformTag, err := formatPlatformReference(ref, p)
			if err != nil {
				return fmt.Errorf("format platform reference failed: %w", err)
			}
			if err = tagImage(ctx, loadedRef, platformTag); err != nil {
				return fmt.Errorf("tag image failed: %w", err)
			}
			img, err := daemon.Image(platformTag)
			if err != nil {
				return fmt.Errorf("load image failed: %w", err)
			}
			slog.DebugContext(ctx, "push image", "ref", platformTag.Name())
			if err := remote.Write(platformTag, img, o.remote...); err != nil {
				return fmt.Errorf("push image failed: %w", err)
			}
			adds = append(adds, mutate.IndexAddendum{
				Add:        img,
				Descriptor: v1.Descriptor{Platform: p},
			})
			return nil
		})
	}
	if err := wg.Wait(); err != nil {
		return fmt.Errorf("push images failed: %w", err)
	}
	slog.DebugContext(ctx, "push manifest", "ref", ref.Name(), "plats", ps)
	return remote.WriteIndex(ref, mutate.AppendManifests(empty.Index, adds...), o.remote...)
}

func buildAndPushImage(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	p *v1.Platform,
	opts ...BuildOption,
) error {
	o := makeBuildOption(opts...)
	loadedRef, err := buildPlatformImage(ctx, buildContext, p, ref)
	if err != nil {
		return fmt.Errorf("build flake image failed: %w", err)
	}
	if loadedRef != ref {
		slog.DebugContext(ctx, "tag image", "ref", ref.Name(), "loadedRef", loadedRef.Name())
		if err = tagImage(ctx, loadedRef, ref); err != nil {
			return fmt.Errorf("tag image failed: %w", err)
		}
	}
	img, err := daemon.Image(ref)
	if err != nil {
		return err
	}
	if o.push {
		slog.DebugContext(ctx, "push image", "ref", ref.Name())
		return remote.Write(ref, img, o.remote...)
	}
	return nil
}

func buildAndPush(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	plats []*v1.Platform,
	opts ...BuildOption,
) error {
	if len(plats) == 1 {
		slog.DebugContext(ctx, "build image", "ref", ref.Name(), "plat", plats[0])
		return buildAndPushImage(
			ctx,
			buildContext,
			ref,
			plats[0],
			opts...,
		)
	}
	slog.DebugContext(ctx, "build image", "ref", ref.Name(), "plats", plats)
	return buildAndPushMultiplatformImage(
		ctx,
		buildContext,
		ref,
		plats,
		opts...,
	)
}
