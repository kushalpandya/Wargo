# Contributing to Wargo

Thanks for helping! Bug reports, ideas and pull requests are all welcome.
For security issues, please use a private
[security advisory](https://github.com/kushalpandya/wargo/security/advisories/new)
instead of a public issue.

## Development setup

Requirements: Go 1.26+, Node.js 22+, a C compiler and PAM headers
(`pam` on Arch, `libpam0g-dev` on Debian/Ubuntu, `pam-devel` on Fedora).

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
light and dark themes — please check both themes for UI changes.

## Guidelines

- Keep it light: Wargo aims to stay a single small binary using little
  memory. Avoid new dependencies unless they clearly pay for themselves.
- Never hold root. Anything privileged goes through the elevation flow
  (`internal/elevate`) and the validated helper (`internal/helper`).
- Every file operation goes through `os.Root`; every command is built as an
  argument list, never a shell string.
- Use the shared UI kit (`web/src/components/ui`) and theme tokens rather than
  raw colours, and keep pages usable on phones.
- Commit titles are short, imperative and sentence-case ("Add …", "Fix …");
  explain the why in a short body.

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
