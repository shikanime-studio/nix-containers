package main

import (
	"log/slog"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/spf13/cobra"
)

var (
	skaffoldCmd = &cobra.Command{
		Use:   "skaffold",
		Short: "Commands for Skaffold integration",
		Long:  "Subcommands intended to be invoked by Skaffold custom builders to build and push images using Nix flakes.",
		Example: "# Build via Skaffold custom builder\n" +
			"./nix-containers skaffold build --accept-flake-config",
	}

	skaffoldBuildCmd = &cobra.Command{
		Use:     "build",
		Short:   "Build and optionally push images",
		Long:    "Builds OCI images from a Nix flake and optionally pushes them to a registry. Configure via env vars: IMAGE, PLATFORMS, BUILD_CONTEXT, PUSH_IMAGE, LOG_LEVEL, ACCEPT_FLAKE_CONFIG.",
		Example: "IMAGE=ghcr.io/you/app:latest PLATFORMS=linux/amd64 PUSH_IMAGE=true BUILD_CONTEXT=. ACCEPT_FLAKE_CONFIG=true ./nix-containers skaffold build",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			buildContext := getBuildContext()
			ref := getImage()
			plats := getPlatforms()
			pushImage := getPushImage()
			acceptFlake := getAcceptFlakeConfig()
			slog.InfoContext(ctx,
				"build config",
				"image", ref.String(),
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
			return buildAndPush(ctx, buildContext, ref, plats, opts...)
		},
	}
)

func init() {
	skaffoldCmd.AddCommand(skaffoldBuildCmd)
	rootCmd.AddCommand(skaffoldCmd)
}
