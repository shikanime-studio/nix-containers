package main

import (
	"slices"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func TestGetPlatformsDeduplicates(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected int
	}{
		{
			name:     "no duplicates",
			input:    []string{"linux/amd64", "linux/arm64"},
			expected: 2,
		},
		{
			name:     "with duplicates",
			input:    []string{"linux/amd64", "linux/arm64", "linux/amd64", "linux/arm64"},
			expected: 2,
		},
		{
			name:     "triple duplicate",
			input:    []string{"linux/amd64", "linux/amd64", "linux/amd64"},
			expected: 1,
		},
		{
			name:     "single platform",
			input:    []string{"linux/amd64"},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plats := make([]*v1.Platform, 0, len(tt.input))
			for _, s := range tt.input {
				p := parsePlatform(s)
				if slices.ContainsFunc(plats, func(existing *v1.Platform) bool {
					return existing.OS == p.OS && existing.Architecture == p.Architecture
				}) {
					continue
				}
				plats = append(plats, p)
			}
			if len(plats) != tt.expected {
				t.Fatalf("expected %d platforms, got %d", tt.expected, len(plats))
			}
		})
	}
}
