# Contributing to Wargo

Thanks for helping! Bug reports, ideas and pull requests are all welcome.
For security issues, please use a private
[security advisory](https://github.com/kushalpandya/wargo/security/advisories/new)
instead of a public issue.

## Development setup

Requirements: Go 1.26+, Node.js 22+, a C compiler and PAM headers
(`pam` on Arch, `libpam0g-dev` on Debian/Ubuntu, `pam-devel` on Fedora).
On macOS you need the Xcode Command Line Tools (`xcode-select --install`) instead of PAM headers, and
If Go is managed with mise, use `mise exec go@1.26.10 -- go ...`.

```sh
git clone https://github.com/kushalpandya/wargo && cd wargo
cd web && npm ci && cd ..

# A local config that skips sign-in (only honoured on 127.0.0.1):
cat > dev.local.toml <<'EOF'
listen = "127.0.0.1:2207"
data_dir = "./data"
[dev]
auth_bypass = true
EOF

go run ./cmd/wargo -config dev.local.toml          # backend on :2207
cd web && WARGO_API=http://127.0.0.1:2207 npm run dev   # UI with live reload on :5173
```

`make` builds the embedded UI and both binaries into `bin/`.

## Before sending a pull request

```sh
go vet ./... && go test ./...
cd web && npx tsc -b
node scripts/audit.mjs http://127.0.0.1:5173/<page>   # layout + accessibility (uses Chrome)
```

`web/scripts/shot.mjs` takes screenshots at phone, tablet and desktop sizes in
light and dark themes. Please check both themes for UI changes.

### Testing on macOS with Colima

The Go code only builds on Linux. On a Mac, create a dedicated Colima profile that mounts only this repo
(`colima start -p wargo --cpu 4 --memory 4 --disk 40 --mount "$PWD"`), then use `scripts/colima-dev.sh test [args]`
to run vet and `go test -race` on Linux, `scripts/colima-dev.sh deploy` to rebuild and restart a `wargo`
container (data kept in the `wargo-data` volume), and `scripts/colima-dev.sh logs` to follow its output.
Your Docker context is left untouched.

## Guidelines

- Keep it light. Wargo should stay a single small binary that uses little
  memory, so only add a dependency when it is clearly worth it.
- Never keep root access. Anything that needs root goes through the approval
  flow (`internal/elevate`) and the checked helper (`internal/helper`).
- Every file operation goes through `os.Root`, and every command is built as
  a list of arguments, never as a shell string.
- Use the shared UI components (`web/src/components/ui`) and theme colours
  instead of fixed colours, and keep pages usable on phones.
- Commit titles are short commands in sentence case ("Add …", "Fix …").
  Explain why in a short body.

## Project layout

```
cmd/wargo           main binary (web service, helper, admin commands)
cmd/wargo-privd     optional root helper for passkey approvals
internal/…          one package per feature (files, docker, term, pkg, …)
internal/host       native vs container mode (paths, users, host commands)
web/                React + Tailwind single-page app, embedded at build time
deploy/             systemd, PAM, reverse proxy and Docker files
docs/               install, security, Docker and proxy guides
```

By contributing you agree that your contributions are licensed under the
[AGPL-3.0](LICENSE).
