# nix-containers

Build OCI images from Nix flakes, with multi-platform and push support.

**Language:** Go + Nix

**Structure:** `main.go` — CLI; `cmd/` — subcommands; `nix/` — build logic; `flake.nix` — dev shell

**Commit style:** Plain-text capitalized title, no prefix. Body with labels: `Design:`, `Related:`, `Closes #`.

**Stack:** 1 commit == 1 PR via ghstack. Amend + `ghstack` to resubmit. `ghstack land` on head PR to land stack. Never `gh pr merge`. Never force-push.

**Protect `main`:** 1 review, linear history, signed commits, squash+rebase only.

*AGPL-3.0. Test with both docker and containerd*
