package main

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func formatArch(s string) string {
	switch s {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "arm32":
		return "armv7l"
	default:
		return s
	}
}

func formatPlatformReference(ref name.Reference, p *v1.Platform) (*name.Tag, error) {
	tag, err := name.NewTag(fmt.Sprintf("%s_%s_%s", ref.Name(), p.OS, p.Architecture))
	if err != nil {
		return nil, fmt.Errorf("failed to format platform reference: %w", err)
	}
	return &tag, nil
}

func formatNixFlakePackageName(ref name.Reference) string {
	repo := ref.Context().RepositoryStr()
	segs := strings.Split(repo, "/")
	return segs[len(segs)-1]
}

func formatNixFlakePackage(buildContext string, ref name.Reference, p *v1.Platform) string {
	return fmt.Sprintf(
		"%s#packages.%s-%s.%s",
		buildContext,
		formatArch(p.Architecture),
		p.OS,
		formatNixFlakePackageName(ref),
	)
}
