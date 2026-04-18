package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"golang.org/x/sync/errgroup"
)

type BuildOption func(*buildOption)

type buildOption struct {
	imageOpts []imageOption
	push      bool
}

type nixBuilderClient interface {
	GetImageBuilderType(
		context.Context,
		string,
		name.Reference,
		*v1.Platform,
		...imageOption,
	) (BuilderType, error)
	BuildPlatformImage(
		context.Context,
		string,
		name.Reference,
		*v1.Platform,
		...imageOption,
	) (string, error)
}

type containerBuilderClient interface {
	CheckPushPermission(name.Reference) error
	TagImage(context.Context, name.Reference, name.Reference) error
	LoadImage(context.Context, name.Reference, string) (name.Reference, error)
	LoadStreamImage(context.Context, name.Reference, string) (name.Reference, error)
	PushImage(name.Reference) error
	PushPlatformImage(name.Reference, *v1.Platform) (mutate.IndexAddendum, error)
	PushManifest(name.Reference, []mutate.IndexAddendum) error
	TrackImage(name.Reference) error
}

type Builder struct {
	nix       nixBuilderClient
	container containerBuilderClient
	imageOpts []imageOption
	push      bool
}

func NewBuilder(
	nix nixBuilderClient,
	container containerBuilderClient,
	opts ...BuildOption,
) *Builder {
	o := makeBuildOption(opts...)
	return &Builder{
		nix:       nix,
		container: container,
		imageOpts: o.imageOpts,
		push:      o.push,
	}
}

func WithStreamImageOption(opt imageOption) BuildOption {
	return func(o *buildOption) { o.imageOpts = append(o.imageOpts, opt) }
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

func (b *Builder) BuildAndPush(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	plats []*v1.Platform,
) error {
	if len(plats) == 0 {
		return fmt.Errorf("at least one platform is required")
	}
	if b.push {
		slog.InfoContext(ctx, "checking push permission", "ref", ref.Name())
		// CheckPushPermission is used to fail fast if the user doesn't have credentials
		// to push to the registry. This prevents running the expensive build process
		// only to fail at the end.
		// See: https://github.com/google/go-containerregistry/issues/412
		if err := b.container.CheckPushPermission(ref); err != nil {
			return err
		}
	}
	if len(plats) == 1 {
		slog.DebugContext(ctx, "build image", "ref", ref.Name(), "plat", plats[0])
		return b.buildAndPushImage(ctx, buildContext, ref, plats[0])
	}
	slog.DebugContext(ctx, "build image", "ref", ref.Name(), "plats", plats)
	return b.buildAndPushMultiplatformImage(ctx, buildContext, ref, plats)
}

func (b *Builder) buildPlatformImage(
	ctx context.Context,
	buildContext string,
	p *v1.Platform,
	ref name.Reference,
) (name.Reference, error) {
	slog.InfoContext(ctx, "build image", "ref", ref.Name(), "os", p.OS, "arch", p.Architecture)

	path, err := b.nix.BuildPlatformImage(
		ctx,
		buildContext,
		ref,
		p,
		b.imageOpts...,
	)
	if err != nil {
		return nil, fmt.Errorf("build image failed: %w", err)
	}

	builderType, err := b.nix.GetImageBuilderType(ctx, buildContext, ref, p, b.imageOpts...)
	if err != nil {
		return nil, fmt.Errorf("check image builder type failed: %w", err)
	}
	slog.InfoContext(
		ctx,
		"image builder type resolved",
		"ref",
		ref.Name(),
		"platform",
		formatSystemName(p),
		"builder_type",
		builderType,
		"path",
		path,
	)

	if builderType == StreamBuilderType {
		slog.InfoContext(
			ctx,
			"load stream image",
			"ref",
			ref.Name(),
			"platform",
			formatSystemName(p),
			"path",
			path,
		)
		return b.container.LoadStreamImage(ctx, ref, path)
	}
	if builderType == TarGzBuilderType {
		slog.InfoContext(
			ctx,
			"load archive image",
			"ref",
			ref.Name(),
			"platform",
			formatSystemName(p),
			"path",
			path,
		)
		return b.container.LoadImage(ctx, ref, path)
	}

	return nil, fmt.Errorf("unknown builder type: %d", builderType)
}

func (b *Builder) buildAndPushMultiplatformImage(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	ps []*v1.Platform,
) error {
	if !b.push {
		return fmt.Errorf(
			"multiplatform image build is only supported when pushing to remote registry",
		)
	}
	var adds []mutate.IndexAddendum
	var addsMu sync.Mutex
	slog.InfoContext(ctx, "build multiplatform image", "ref", ref.Name(), "platform_count", len(ps))
	wg, ctx := errgroup.WithContext(ctx)
	for _, p := range ps {
		p := p
		wg.Go(func() error {
			slog.InfoContext(
				ctx,
				"platform pipeline started",
				"ref",
				ref.Name(),
				"platform",
				formatSystemName(p),
			)
			loadedRef, err := b.buildPlatformImage(ctx, buildContext, p, ref)
			if err != nil {
				return err
			}
			slog.InfoContext(
				ctx,
				"platform image loaded",
				"ref",
				ref.Name(),
				"platform",
				formatSystemName(p),
				"loaded_ref",
				loadedRef.Name(),
			)
			platformTag, err := formatPlatformReference(ref, p)
			if err != nil {
				return fmt.Errorf("format platform reference failed: %w", err)
			}
			slog.InfoContext(
				ctx,
				"tag platform image",
				"ref",
				ref.Name(),
				"platform",
				formatSystemName(p),
				"platform_ref",
				platformTag.Name(),
			)
			if err = b.container.TagImage(ctx, loadedRef, platformTag); err != nil {
				return fmt.Errorf("tag image failed: %w", err)
			}
			slog.InfoContext(
				ctx,
				"platform image tagged",
				"ref",
				ref.Name(),
				"platform",
				formatSystemName(p),
				"platform_ref",
				platformTag.Name(),
			)
			slog.InfoContext(
				ctx,
				"push platform image",
				"ref",
				ref.Name(),
				"platform",
				formatSystemName(p),
				"platform_ref",
				platformTag.Name(),
			)
			add, err := b.container.PushPlatformImage(platformTag, p)
			if err != nil {
				return err
			}
			slog.InfoContext(
				ctx,
				"platform image pushed",
				"ref",
				ref.Name(),
				"platform",
				formatSystemName(p),
				"platform_ref",
				platformTag.Name(),
			)
			addsMu.Lock()
			adds = append(adds, add)
			addsMu.Unlock()
			slog.InfoContext(
				ctx,
				"platform pipeline completed",
				"ref",
				ref.Name(),
				"platform",
				formatSystemName(p),
			)
			return nil
		})
	}
	if err := wg.Wait(); err != nil {
		return fmt.Errorf("push images failed: %w", err)
	}
	slog.InfoContext(ctx, "push manifest", "ref", ref.Name(), "platform_count", len(adds))
	if err := b.container.PushManifest(ref, adds); err != nil {
		return err
	}
	slog.InfoContext(ctx, "manifest pushed", "ref", ref.Name(), "platform_count", len(adds))
	if err := b.container.TrackImage(ref); err != nil {
		return err
	}
	slog.InfoContext(ctx, "manifest written to daemon", "ref", ref.Name())
	return nil
}

func (b *Builder) buildAndPushImage(
	ctx context.Context,
	buildContext string,
	ref name.Reference,
	p *v1.Platform,
) error {
	loadedRef, err := b.buildPlatformImage(ctx, buildContext, p, ref)
	if err != nil {
		return fmt.Errorf("build flake image failed: %w", err)
	}
	if loadedRef != ref {
		slog.DebugContext(ctx, "tag image", "ref", ref.Name(), "loadedRef", loadedRef.Name())
		if err = b.container.TagImage(ctx, loadedRef, ref); err != nil {
			return fmt.Errorf("tag image failed: %w", err)
		}
	}
	if b.push {
		slog.DebugContext(ctx, "push image", "ref", ref.Name())
		if err := b.container.PushImage(ref); err != nil {
			return err
		}
	}
	return nil
}
