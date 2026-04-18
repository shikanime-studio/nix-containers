package main

import (
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func TestFormatSystemName(t *testing.T) {
	got := formatSystemName(&v1.Platform{OS: "linux", Architecture: "arm64"})
	if got != "aarch64-linux" {
		t.Fatalf("expected aarch64-linux, got %s", got)
	}
}

func TestFormatNixFlakePackage(t *testing.T) {
	ref, err := name.ParseReference("ghcr.io/shikanime/shikanime/catbox:latest")
	if err != nil {
		t.Fatalf("parse reference failed: %v", err)
	}

	got := formatNixFlakePackage(
		"/workspace",
		ref,
		&v1.Platform{OS: "linux", Architecture: "amd64"},
	)
	want := "/workspace#packages.x86_64-linux.catbox"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}
