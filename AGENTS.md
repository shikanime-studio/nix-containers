# nix-containers

Build OCI images from Nix flakes, with optional multi-platform output and push to registries. Designed for use as a Skaffold custom builder, but usable directly from the CLI.

**Language:** Go (Nix for build definitions)

## Structure

- `main.go` — CLI entry point
- `cmd/` — Subcommand implementations
- `nix/` — Nix build logic
- `flake.nix` — Development shell

## Commit Style

- Plain-text capitalized title, no conventional-commit prefix
- Body with labels: `Design:`, `Related:`, `Closes #`
- Keep Markdown lines wrapped at 80 columns and run `nix fmt` before shipping

## Stack

- 1 commit == 1 PR via ghstack
- Amend + `ghstack` to resubmit
- `ghstack land` on head PR to land the entire stack
- Never `gh pr merge` (creates poisoned commits)
- Never force-push ghstack branches
- ghstack only works on HEAD commit chains, not detached HEADs

## Protect `main`

- Require 1 approving review
- Require linear history (no merge commits)
- Require signed commits
- Squash+rebase merge only

*Licensed under AGPL-3.0. Test with both `docker` and `containerd` runtimes*