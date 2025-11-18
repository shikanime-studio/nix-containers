package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
			image := getImage()
			plats := getPlatforms()
			pushImage := getPushImage()
			acceptFlake := getAcceptFlakeConfig()
			buildContext := ""
			if len(args) > 0 {
				buildContext = args[0]
			} else {
				buildContext = getBuildContext()
			}
			if buildContext == "" {
				return fmt.Errorf(
					"build context must be provided via arg or --build-context/BUILD_CONTEXT",
				)
			}
			slog.Info(
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
			return buildAndPushMultiplatformImage(
				context.Background(),
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
	slog.SetDefault(slog.New(slog.NewTextHandler(
		os.Stderr,
		&slog.HandlerOptions{Level: getLogLevel()},
	)))
	if err := rootCmd.Execute(); err != nil {
		slog.Error("command failed", "err", err)
		os.Exit(1)
	}
}

func buildFlakeImage(
	ctx context.Context,
	buildContext string,
	p *v1.Platform,
	ref name.Reference,
	opts ...BuildOption,
) (v1.Image, error) {
	o := &buildOption{}
	for _, opt := range opts {
		opt(o)
	}
	slog.Info("build image", "ref", ref.Name(), "os", p.OS, "arch", p.Architecture)
	path, err := buildStreamLayeredImage(
		ctx,
		formatNixFlakePackage(buildContext, ref, p),
		o.layered...,
	)
	if err != nil {
		return nil, fmt.Errorf("build stream layered image failed: %w", err)
	}
	return imageFromStreamLayeredImage(ctx, path)
}

func buildPlatformImage(
	ctx context.Context,
	buildContext string,
	p *v1.Platform,
	ref name.Reference,
) (name.Reference, v1.Image, error) {
	img, err := buildFlakeImage(ctx, buildContext, p, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("build flake image failed: %w", err)
	}
	ref, err = name.NewTag(formatPlatformReference(ref, p))
	if err != nil {
		return nil, nil, fmt.Errorf("create platform reference failed: %w", err)
	}
	return ref, img, nil
}

func buildAndPushMultiplatformImage(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	ps []*v1.Platform,
	opts ...BuildOption,
) error {
	o := &buildOption{}
	for _, opt := range opts {
		opt(o)
	}
	var adds []mutate.IndexAddendum
	for _, p := range ps {
		plaformRef, img, err := buildPlatformImage(ctx, buildContext, p, ref)
		if err != nil {
			return err
		}
		if o.push {
			if err := remote.Write(plaformRef, img, o.remote...); err != nil {
				return err
			}
		}
		adds = append(adds, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: p},
		})
	}
	if o.push {
		return remote.WriteIndex(ref, mutate.AppendManifests(empty.Index, adds...), o.remote...)
	}
	return nil
}

func buildAndPushImage(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	p *v1.Platform,
	opts ...BuildOption,
) error {
	o := &buildOption{}
	for _, opt := range opts {
		opt(o)
	}
	img, err := buildFlakeImage(ctx, buildContext, p, ref, opts...)
	if err != nil {
		return fmt.Errorf("build flake image failed: %w", err)
	}
	if o.push {
		return remote.Write(ref, img, o.remote...)
	}
	return nil
}

func build(
	ctx context.Context,
	buildContext string,
	plats []*v1.Platform,
	opts ...BuildOption,
) error {
	if len(plats) == 1 {
		return buildAndPushImage(
			ctx,
			buildContext,
			getImage(),
			plats[0],
			opts...,
		)
	}
	return buildAndPushMultiplatformImage(
		ctx,
		buildContext,
		getImage(),
		plats,
		opts...,
	)
}
