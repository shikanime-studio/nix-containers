// CLI to build and push OCI images from Nix flakes
package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/viper"
)

func init() {
	viper.AutomaticEnv()
	if err := viper.BindEnv("build_context", "BUILD_CONTEXT"); err != nil {
		slog.Error("bind env failed", "env", "BUILD_CONTEXT", "key", "build_context", "err", err)
		os.Exit(1)
	}
	if err := viper.BindEnv("image", "IMAGE"); err != nil {
		slog.Error("bind env failed", "env", "IMAGE", "key", "image", "err", err)
		os.Exit(1)
	}
	if err := viper.BindEnv("platforms", "PLATFORMS"); err != nil {
		slog.Error("bind env failed", "env", "PLATFORMS", "key", "platforms", "err", err)
		os.Exit(1)
	}
	if err := viper.BindEnv("push_image", "PUSH_IMAGE"); err != nil {
		slog.Error("bind env failed", "env", "PUSH_IMAGE", "key", "push_image", "err", err)
		os.Exit(1)
	}
	if err := viper.BindEnv("log_level", "LOG_LEVEL"); err != nil {
		slog.Error("bind env failed", "env", "LOG_LEVEL", "key", "log_level", "err", err)
		os.Exit(1)
	}
	if err := viper.BindEnv("accept_flake_config", "ACCEPT_FLAKE_CONFIG"); err != nil {
		slog.Error(
			"bind env failed",
			"env",
			"ACCEPT_FLAKE_CONFIG",
			"key",
			"accept_flake_config",
			"err",
			err,
		)
		os.Exit(1)
	}
}

func getHostPlatform() *v1.Platform {
	return &v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
}

func parsePlatform(s string) *v1.Platform {
	seg := strings.SplitN(s, "/", 2)
	operatingSystem := ""
	arch := ""
	if len(seg) > 0 {
		operatingSystem = seg[0]
	}
	if len(seg) > 1 {
		arch = seg[1]
	}
	return &v1.Platform{OS: operatingSystem, Architecture: arch}
}

func getPlatforms() []*v1.Platform {
	v := viper.GetString("platforms")
	if v == "" {
		hp := getHostPlatform()
		slog.Info("no platforms specified", "detected_os", hp.OS, "detected_arch", hp.Architecture)
		return []*v1.Platform{hp}
	}
	ps := strings.Split(v, ",")
	plats := make([]*v1.Platform, 0, len(ps))
	for _, s := range ps {
		plats = append(plats, parsePlatform(s))
	}
	return plats
}

func getPushImage() bool {
	switch strings.ToLower(viper.GetString("push_image")) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func getBuildContext() string {
	return viper.GetString("build_context")
}

func getImageTag() (name.Tag, error) {
	s := viper.GetString("image")
	ref, err := name.NewTag(s)
	if err != nil {
		return name.Tag{}, fmt.Errorf("invalid image reference: %w", err)
	}
	return ref, nil
}

func getLogLevel() (slog.Level, error) {
	v := strings.ToLower(viper.GetString("log_level"))
	switch v {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level: %s", v)
	}
}

func getAcceptFlakeConfig() bool {
	switch strings.ToLower(viper.GetString("accept_flake_config")) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
