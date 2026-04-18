package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

var commandStubMu sync.Mutex

func stubCommand(
	t testing.TB,
	stdout, stderr string,
	exitCode int,
	argsFile string,
) func(context.Context, string, ...string) *exec.Cmd {
	t.Helper()

	return func(ctx context.Context, command string, args ...string) *exec.Cmd {
		cmdArgs := make([]string, 0, 3+len(args))
		cmdArgs = append(cmdArgs, "-test.run=^TestHelperProcess$", "--", command)
		cmdArgs = append(cmdArgs, args...)

		cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
		cmd.Env = append(
			os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			fmt.Sprintf("FAKE_STDOUT=%s", stdout),
			fmt.Sprintf("FAKE_STDERR=%s", stderr),
			fmt.Sprintf("FAKE_EXIT_CODE=%d", exitCode),
		)
		if argsFile != "" {
			cmd.Env = append(cmd.Env, fmt.Sprintf("FAKE_ARGS_FILE=%s", argsFile))
		}
		return cmd
	}
}

func setupNixCommandTest(t testing.TB, stdout, stderr string, exitCode int) string {
	t.Helper()

	commandStubMu.Lock()
	originalExec := nixCommandContext
	t.Cleanup(func() {
		nixCommandContext = originalExec
		commandStubMu.Unlock()
	})

	argsFile := filepath.Join(t.TempDir(), "args.json")
	nixCommandContext = stubCommand(t, stdout, stderr, exitCode, argsFile)
	return argsFile
}

func readCapturedCommandArgs(t testing.TB, argsFile string) []string {
	t.Helper()

	argsRaw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file failed: %v", err)
	}

	var args []string
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		t.Fatalf("decode args file failed: %v", err)
	}
	return args
}

func assertCapturedCommandArgs(t testing.TB, argsFile string, want ...string) {
	t.Helper()

	got := readCapturedCommandArgs(t, argsFile)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected captured args %q, got %q", want, got)
	}
}

func TestHelperProcess(_ *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	sep := 0
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep == 0 || sep+1 >= len(args) {
		if _, err := fmt.Fprint(os.Stderr, "missing helper separator"); err != nil {
			os.Exit(2)
		}
		os.Exit(2)
	}

	if argsFile := os.Getenv("FAKE_ARGS_FILE"); argsFile != "" {
		cmdArgs, err := json.Marshal(args[sep+1:])
		if err != nil {
			if _, writeErr := fmt.Fprint(os.Stderr, err.Error()); writeErr != nil {
				os.Exit(2)
			}
			os.Exit(2)
		}
		if err := os.WriteFile(argsFile, cmdArgs, 0o644); err != nil {
			if _, writeErr := fmt.Fprint(os.Stderr, err.Error()); writeErr != nil {
				os.Exit(2)
			}
			os.Exit(2)
		}
	}

	if _, err := fmt.Fprint(os.Stdout, os.Getenv("FAKE_STDOUT")); err != nil {
		os.Exit(2)
	}
	if _, err := fmt.Fprint(os.Stderr, os.Getenv("FAKE_STDERR")); err != nil {
		os.Exit(2)
	}

	code, err := strconv.Atoi(os.Getenv("FAKE_EXIT_CODE"))
	if err != nil {
		code = 0
	}
	os.Exit(code)
}

func TestNixClientGetImageBuilderTypeParsesStreamArtifact(t *testing.T) {
	argsFile := setupNixCommandTest(
		t,
		`{"packages":{"x86_64-linux":{"app":{"name":"stream-app","type":"derivation"}}}}`,
		"",
		0,
	)

	ref := mustParseReference(t, "ghcr.io/example/app:latest")
	builderType, err := NewNixClient().GetImageBuilderType(
		context.Background(),
		"/workspace",
		ref,
		&v1.Platform{OS: "linux", Architecture: "amd64"},
	)
	if err != nil {
		t.Fatalf("get image builder type failed: %v", err)
	}
	if builderType != StreamBuilderType {
		t.Fatalf("expected stream builder type, got %d", builderType)
	}

	assertCapturedCommandArgs(
		t,
		argsFile,
		"nix",
		"flake",
		"show",
		"--json",
		"--all-systems",
		"/workspace",
		"--no-pure-eval",
	)
}

func TestNixClientBuildImageReturnsOutPath(t *testing.T) {
	argsFile := setupNixCommandTest(
		t,
		`[{"drvPath":"/nix/store/app.drv","outputs":{"out":"/nix/store/app"}}]`,
		"building\n",
		0,
	)

	got, err := NewNixClient().BuildImage(context.Background(), "/workspace#packages.x86_64-linux.app")
	if err != nil {
		t.Fatalf("build image failed: %v", err)
	}
	if got != "/nix/store/app" {
		t.Fatalf("expected /nix/store/app, got %s", got)
	}

	assertCapturedCommandArgs(
		t,
		argsFile,
		"nix",
		"build",
		"--accept-flake-config",
		"--no-link",
		"--json",
		"/workspace#packages.x86_64-linux.app",
	)
}

func TestNixClientBuildImageReturnsErrorOnEmptyResult(t *testing.T) {
	argsFile := setupNixCommandTest(t, `[]`, "", 0)

	_, err := NewNixClient().BuildImage(context.Background(), "/workspace#packages.x86_64-linux.app")
	if err == nil || !strings.Contains(err.Error(), "no output path found") {
		t.Fatalf("expected empty result error, got %v", err)
	}
	assertCapturedCommandArgs(
		t,
		argsFile,
		"nix",
		"build",
		"--accept-flake-config",
		"--no-link",
		"--json",
		"/workspace#packages.x86_64-linux.app",
	)
}

func TestNixClientBuildImageReturnsStderrOnCommandFailure(t *testing.T) {
	argsFile := setupNixCommandTest(
		t,
		`[{"drvPath":"/nix/store/app.drv","outputs":{"out":"/nix/store/app"}}]`,
		"build failed\nhint: inspect logs",
		1,
	)

	_, err := NewNixClient().BuildImage(context.Background(), "/workspace#packages.x86_64-linux.app")
	if err == nil {
		t.Fatal("expected build failure")
	}
	if !strings.Contains(err.Error(), "failed to wait for command") {
		t.Fatalf("expected wait error, got %v", err)
	}
	if !strings.Contains(err.Error(), "build failed\nhint: inspect logs") {
		t.Fatalf("expected stderr in error, got %v", err)
	}

	assertCapturedCommandArgs(
		t,
		argsFile,
		"nix",
		"build",
		"--accept-flake-config",
		"--no-link",
		"--json",
		"/workspace#packages.x86_64-linux.app",
	)
}

func TestNixClientBuildPlatformImageFormatsFlakeTarget(t *testing.T) {
	argsFile := setupNixCommandTest(
		t,
		`[{"drvPath":"/nix/store/app.drv","outputs":{"out":"/nix/store/app"}}]`,
		"",
		0,
	)

	ref, err := name.ParseReference("ghcr.io/example/app:latest")
	if err != nil {
		t.Fatalf("parse reference failed: %v", err)
	}
	got, err := NewNixClient().BuildPlatformImage(
		context.Background(),
		"/workspace",
		ref,
		&v1.Platform{OS: "linux", Architecture: "amd64"},
	)
	if err != nil {
		t.Fatalf("build platform image failed: %v", err)
	}
	if got != "/nix/store/app" {
		t.Fatalf("expected /nix/store/app, got %s", got)
	}

	assertCapturedCommandArgs(
		t,
		argsFile,
		"nix",
		"build",
		"--accept-flake-config",
		"--no-link",
		"--json",
		"/workspace#packages.x86_64-linux.app",
	)
}
