# nix-containers

Build OCI images from Nix flakes, with optional multi-platform output and push to registries. Designed for use as a Skaffold custom builder, but usable directly from the CLI as well.

## Installation

- Go install: `go install github.com/shikanime/nix-containers@latest`
- From source: `go build -o nix-containers .`

## Commands

- `nix-containers build [BUILD_CONTEXT]`
  - Builds images from the flake at `BUILD_CONTEXT` (positional, e.g., `.`) and optionally pushes.
- `nix-containers skaffold build`
  - Intended for Skaffold custom builders; reads `BUILD_CONTEXT` from env.

## Flags

- Global:
  - `--accept-flake-config` Accept Nix flake configuration during build (also via `ACCEPT_FLAKE_CONFIG`).
- Build command:
  - `--platforms` Comma-separated platforms in `os/arch` form (e.g., `linux/amd64,linux/arm64`). Overrides `PLATFORMS` env.

## Environment Variables

- `IMAGE` Required. Target image reference (e.g., `ghcr.io/you/app:latest`).
- `PLATFORMS` Optional. Comma-separated platforms (`linux/amd64,linux/arm64`). Defaults to host arch when unset. Overridden by `--platforms`.
- `BUILD_CONTEXT` Used by `skaffold build` (path to flake). For `build`, pass as positional argument.
- `PUSH_IMAGE` Optional boolean (`true|false|1|yes|on`). When true, images are pushed after build.
- `LOG_LEVEL` Optional (`info|debug|warn|error`). Defaults to `info`.
- `ACCEPT_FLAKE_CONFIG` Optional boolean. Accept Nix flake config during build. Can also be set via `--accept-flake-config`.

## Examples

### Direct CLI

- Single platform build (no push):
  - `IMAGE=ghcr.io/you/app:latest ./nix-containers build .`

- Multi-platform build and push via env:
  - `IMAGE=ghcr.io/you/app:latest PLATFORMS=linux/amd64,linux/arm64 PUSH_IMAGE=true ./nix-containers build .`

- Multi-platform build via flag and accept flake config:
  - `IMAGE=ghcr.io/you/app:latest PUSH_IMAGE=true ./nix-containers build --platforms linux/amd64,linux/arm64 --accept-flake-config .`

### Skaffold Usage

```yaml
apiVersion: skaffold/v4beta11
kind: Config
metadata:
  name: nix-containers
build:
  artifacts:
    - image: ghcr.io/you/app:latest
      custom:
        buildCommand: ./nix-containers skaffold build --accept-flake-config
deploy:
  kubectl:
    manifests:
      - k8s/*.yaml
```

At build time, set the following env vars for the custom builder:

- `IMAGE=ghcr.io/you/app:latest`
- `BUILD_CONTEXT=.`
- `PLATFORMS=linux/amd64,linux/arm64` (or use `--platforms` in `buildCommand`)
- `PUSH_IMAGE=true`

## Notes

- Authentication uses Docker credential helpers via the default keychain.
- When building multi-platform images with push enabled, individual platform images are pushed first, then a multi-arch index is written.
