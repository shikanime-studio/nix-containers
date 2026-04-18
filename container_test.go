package main

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/authn"
)

type testContextKey string

type fakeKeychain struct{}

func (fakeKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.Anonymous, nil
}

type fakeRoundTripper struct{}

func (fakeRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}

func TestNewContainerClientUsesInjectedFactoryAndOptions(t *testing.T) {
	originalFactory := newDockerClient
	t.Cleanup(func() { newDockerClient = originalFactory })

	wantClient := &client.Client{}
	var gotCtx context.Context
	newDockerClient = func(ctx context.Context) (*client.Client, error) {
		gotCtx = ctx
		return wantClient, nil
	}

	ctx := context.WithValue(context.Background(), testContextKey("test"), "value")
	keychain := fakeKeychain{}
	transport := fakeRoundTripper{}

	containerClient, err := NewContainerClient(
		ctx,
		WithContainerKeychain(keychain),
		WithContainerTransport(transport),
	)
	if err != nil {
		t.Fatalf("create container client failed: %v", err)
	}

	if gotCtx != ctx {
		t.Fatalf("expected factory to receive original context")
	}
	if containerClient.docker != wantClient {
		t.Fatalf("expected injected docker client to be preserved")
	}
	if containerClient.keychain != keychain {
		t.Fatalf("expected keychain override to be stored")
	}
	if _, ok := containerClient.transport.(fakeRoundTripper); !ok {
		t.Fatalf("expected transport override to be stored")
	}
	if len(containerClient.remote) != 4 {
		t.Fatalf(
			"expected default and override remote options, got %d",
			len(containerClient.remote),
		)
	}
}

func TestNewContainerClientWrapsFactoryError(t *testing.T) {
	originalFactory := newDockerClient
	t.Cleanup(func() { newDockerClient = originalFactory })

	newDockerClient = func(context.Context) (*client.Client, error) {
		return nil, errors.New("boom")
	}

	_, err := NewContainerClient(context.Background())
	if err == nil || !strings.Contains(err.Error(), "create docker client failed: boom") {
		t.Fatalf("expected wrapped factory error, got %v", err)
	}
}

func TestReadImageLoadedRefParsesResultAfterProgress(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(
		"{\"status\":\"Loading layer\",\"progress\":\"1/1\",\"id\":\"sha256:abc\"}\n" +
			"{\"stream\":\"Loaded image: ghcr.io/example/app:latest\\n\"}\n",
	))

	ref, err := readImageLoadedRef(context.Background(), reader)
	if err != nil {
		t.Fatalf("read loaded ref failed: %v", err)
	}
	if got := ref.Name(); got != "ghcr.io/example/app:latest" {
		t.Fatalf("expected loaded ref ghcr.io/example/app:latest, got %s", got)
	}
}
