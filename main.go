package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

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
			debug := getDebug()
			if debug {
				slog.SetLogLoggerLevel(slog.LevelDebug)
			}
			image, err := getImageTag()
			if err != nil {
				return fmt.Errorf("failed to get image: %w", err)
			}
			plats := getPlatforms()
			pushImage := getPushImage()
			acceptFlake := getAcceptFlakeConfig()
			noPureEval := getNoPureEval()
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
				"no_pure_eval", noPureEval,
			)
			opts := []BuildOption{
				WithPush(pushImage),
			}
			if acceptFlake {
				opts = append(opts, WithStreamImageOption(WithAcceptFlakeConfig()))
			}
			if noPureEval {
				opts = append(opts, WithStreamImageOption(WithNoPureEval()))
			}
			container, err := NewContainerClient(ctx)
			if err != nil {
				return fmt.Errorf("failed to create container client: %w", err)
			}
			builder := NewBuilder(NewNixClient(), container, opts...)
			return builder.BuildAndPush(ctx, buildContext, image, plats)
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
	rootCmd.PersistentFlags().
		Bool("no-pure-eval", false, "disable pure evaluation of nix expressions")
	if err := viper.BindPFlag("no_pure_eval", rootCmd.PersistentFlags().Lookup("no-pure-eval")); err != nil {
		slog.Error("bind flag failed", "flag", "no-pure-eval", "err", err)
		os.Exit(1)
	}
	rootCmd.PersistentFlags().
		Bool("debug", false, "enable debug logging")
	if err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug")); err != nil {
		slog.Error("bind flag failed", "flag", "debug", "err", err)
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
