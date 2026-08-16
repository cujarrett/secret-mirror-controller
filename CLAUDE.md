See [AGENTS.md](./AGENTS.md) for project structure and kubebuilder specifics.

## Rules

- **Never run `git add`, `git commit`, `git push`, or any git command that writes to the index, history, or remotes.** Output the commands for the user to run - staging is part of their review.
- **Always suggest a commit message** when work is ready to commit.
- **Give `git add` and the commit as two separate steps, listing every file explicitly** - never `git add .` or a bare directory.
- **Always precede `git add` with the `cd` to this repo's absolute path.**
- **Never output a `git push` command.**
- **Never use em dashes.** Use a hyphen, comma, full stop, or brackets.
- Before telling the user to commit, run `/security-review`. Report one line if clean; spend words only on real findings.

## Build

`make` is the entrypoint, not `just`. The other Go repos in this workspace use `just` because they have no build tool of their own - this one is generated around kubebuilder's Makefile, and the shipped GitHub workflows call it directly. Adding a second entrypoint would just be a wrapper to keep in sync.

| Target | What it does |
|---|---|
| `make generate manifests` | regenerate deepcopy, CRD and RBAC after editing `api/` - run before staging |
| `make test` | `go test` including the envtest suite, downloading API server binaries first |
| `make lint` | `golangci-lint run` |
| `make build` | build the manager binary into `bin/` |
| `make run` | run against the current kubeconfig |
| `make install` / `make uninstall` | apply or remove the CRD |
| `make docker-build docker-push` | build and push the image |

The lab's source namespace is not the default, so run it as `go run ./cmd --source-namespace=mirror-src`.

Fast feedback loop without envtest binaries - `go test -race -skip TestControllers ./...`.

Never hand-edit `config/` or `zz_generated.deepcopy.go`.

## Comment style

Two or three lines, never a paragraph. Say **why** the code is the way it is, never what the line already says. No "used to", "was previously", "this replaces". If the code is unclear, fix the code.

## Philosophy

Grug-brained: say no to complexity, no abstraction until a pattern repeats three times, boring obvious code wins, understand why code exists before removing it.
