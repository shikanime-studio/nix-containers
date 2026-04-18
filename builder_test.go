//go:generate go run github.com/matryer/moq@v0.7.1 -rm -stub -out builder_moq_test.go . nixBuilderClient:mockNixBuilderClient containerBuilderClient:mockContainerBuilderClient

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

func mustParseReference(t *testing.T, raw string) name.Reference {
	t.Helper()

	ref, err := name.ParseReference(raw)
	if err != nil {
		t.Fatalf("parse reference failed: %v", err)
	}
	return ref
}

func TestBuilderBuildAndPushReturnsPermissionErrorBeforeBuild(t *testing.T) {
	ref := mustParseReference(t, "ghcr.io/example/app:latest")
	plat := &v1.Platform{OS: "linux", Architecture: "amd64"}
	nixClient := &mockNixBuilderClient{}
	containerClient := &mockContainerBuilderClient{
		CheckPushPermissionFunc: func(name.Reference) error {
			return errors.New("no credentials")
		},
	}

	builder := NewBuilder(nixClient, containerClient, WithPush(true))
	err := builder.BuildAndPush(context.Background(), "/workspace", ref, []*v1.Platform{plat})
	if err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("expected permission error, got %v", err)
	}
	if len(nixClient.BuildPlatformImageCalls()) != 0 {
		t.Fatalf(
			"expected nix build to be skipped, got %d calls",
			len(nixClient.BuildPlatformImageCalls()),
		)
	}
	if len(containerClient.CheckPushPermissionCalls()) != 1 {
		t.Fatalf(
			"expected one permission check, got %d",
			len(containerClient.CheckPushPermissionCalls()),
		)
	}
}

func TestBuilderBuildAndPushSinglePlatformStreamFlow(t *testing.T) {
	ref := mustParseReference(t, "ghcr.io/example/app:latest")
	loadedRef := mustParseReference(t, "ghcr.io/example/app:loaded")
	plat := &v1.Platform{OS: "linux", Architecture: "amd64"}
	nixClient := &mockNixBuilderClient{
		BuildPlatformImageFunc: func(context.Context, string, name.Reference, *v1.Platform, ...imageOption) (string, error) {
			return "/tmp/result", nil
		},
		GetImageBuilderTypeFunc: func(context.Context, string, name.Reference, *v1.Platform, ...imageOption) (BuilderType, error) {
			return StreamBuilderType, nil
		},
	}
	containerClient := &mockContainerBuilderClient{
		LoadStreamImageFunc: func(context.Context, name.Reference, string) (name.Reference, error) {
			return loadedRef, nil
		},
	}

	builder := NewBuilder(
		nixClient,
		containerClient,
		WithPush(true),
		WithStreamImageOption(WithAcceptFlakeConfig()),
	)
	if err := builder.BuildAndPush(context.Background(), "/workspace", ref, []*v1.Platform{plat}); err != nil {
		t.Fatalf("build and push failed: %v", err)
	}

	buildCalls := nixClient.BuildPlatformImageCalls()
	typeCalls := nixClient.GetImageBuilderTypeCalls()
	if len(buildCalls) != 1 || len(typeCalls) != 1 {
		t.Fatalf(
			"expected one nix build/type check, got build=%d type=%d",
			len(buildCalls),
			len(typeCalls),
		)
	}
	if len(buildCalls[0].ImageOptionMoqParams) != 1 || len(typeCalls[0].ImageOptionMoqParams) != 1 {
		t.Fatalf("expected image options to flow through builder")
	}
	loadStreamCalls := containerClient.LoadStreamImageCalls()
	if len(loadStreamCalls) != 1 || loadStreamCalls[0].S != "/tmp/result" {
		t.Fatalf(
			"expected stream load from /tmp/result, got calls=%d path=%q",
			len(loadStreamCalls),
			loadStreamCalls[0].S,
		)
	}
	if len(containerClient.LoadImageCalls()) != 0 {
		t.Fatalf(
			"expected archive loader to be unused, got %d calls",
			len(containerClient.LoadImageCalls()),
		)
	}
	tagCalls := containerClient.TagImageCalls()
	if len(tagCalls) != 1 || tagCalls[0].Reference1.Name() != loadedRef.Name() ||
		tagCalls[0].Reference2.Name() != ref.Name() {
		t.Fatalf("expected image tag from %s to %s", loadedRef.Name(), ref.Name())
	}
	pushImageCalls := containerClient.PushImageCalls()
	if len(pushImageCalls) != 1 || pushImageCalls[0].Reference.Name() != ref.Name() {
		t.Fatalf(
			"expected pushed ref %s, got calls=%d ref=%v",
			ref.Name(),
			len(pushImageCalls),
			pushImageCalls[0].Reference,
		)
	}
}

func TestBuilderBuildAndPushMultiplatformRequiresPush(t *testing.T) {
	ref := mustParseReference(t, "ghcr.io/example/app:latest")
	plats := []*v1.Platform{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "arm64"},
	}

	builder := NewBuilder(&mockNixBuilderClient{}, &mockContainerBuilderClient{}, WithPush(false))
	err := builder.BuildAndPush(context.Background(), "/workspace", ref, plats)
	if err == nil || !strings.Contains(err.Error(), "only supported when pushing") {
		t.Fatalf("expected multiplatform push error, got %v", err)
	}
}

func TestBuilderBuildAndPushRejectsEmptyPlatforms(t *testing.T) {
	ref := mustParseReference(t, "ghcr.io/example/app:latest")
	containerClient := &mockContainerBuilderClient{}

	builder := NewBuilder(&mockNixBuilderClient{}, containerClient, WithPush(true))
	err := builder.BuildAndPush(context.Background(), "/workspace", ref, nil)
	if err == nil || !strings.Contains(err.Error(), "at least one platform is required") {
		t.Fatalf("expected empty platform error, got %v", err)
	}
	if len(containerClient.CheckPushPermissionCalls()) != 0 {
		t.Fatalf(
			"expected push permission check to be skipped, got %d",
			len(containerClient.CheckPushPermissionCalls()),
		)
	}
}

func TestBuilderBuildAndPushMultiplatformTracksImage(t *testing.T) {
	ref := mustParseReference(t, "ghcr.io/example/app:latest")
	loadedRef := mustParseReference(t, "ghcr.io/example/app:loaded")
	plats := []*v1.Platform{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "arm64"},
	}
	nixClient := &mockNixBuilderClient{
		BuildPlatformImageFunc: func(context.Context, string, name.Reference, *v1.Platform, ...imageOption) (string, error) {
			return "/tmp/result", nil
		},
		GetImageBuilderTypeFunc: func(context.Context, string, name.Reference, *v1.Platform, ...imageOption) (BuilderType, error) {
			return TarGzBuilderType, nil
		},
	}
	containerClient := &mockContainerBuilderClient{
		LoadImageFunc: func(context.Context, name.Reference, string) (name.Reference, error) {
			return loadedRef, nil
		},
		PushPlatformImageFunc: func(name.Reference, *v1.Platform) (mutate.IndexAddendum, error) {
			return mutate.IndexAddendum{}, nil
		},
	}

	builder := NewBuilder(nixClient, containerClient, WithPush(true))
	if err := builder.BuildAndPush(context.Background(), "/workspace", ref, plats); err != nil {
		t.Fatalf("multiplatform build and push failed: %v", err)
	}

	if len(nixClient.BuildPlatformImageCalls()) != 2 ||
		len(nixClient.GetImageBuilderTypeCalls()) != 2 {
		t.Fatalf(
			"expected one nix build/type per platform, got build=%d type=%d",
			len(nixClient.BuildPlatformImageCalls()),
			len(nixClient.GetImageBuilderTypeCalls()),
		)
	}
	if len(containerClient.LoadImageCalls()) != 2 {
		t.Fatalf("expected two archive loads, got %d", len(containerClient.LoadImageCalls()))
	}
	if len(containerClient.PushPlatformImageCalls()) != 2 {
		t.Fatalf(
			"expected two platform pushes, got %d",
			len(containerClient.PushPlatformImageCalls()),
		)
	}
	if len(containerClient.PushManifestCalls()) != 1 {
		t.Fatalf("expected one manifest push, got %d", len(containerClient.PushManifestCalls()))
	}
	trackImageCalls := containerClient.TrackImageCalls()
	if len(trackImageCalls) != 1 || trackImageCalls[0].Reference.Name() != ref.Name() {
		t.Fatalf(
			"expected tracked ref %s, got calls=%d ref=%v",
			ref.Name(),
			len(trackImageCalls),
			trackImageCalls,
		)
	}
}
