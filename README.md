# Wargo demo site (gh-pages branch)

This branch holds the **demo** of [Wargo](https://github.com/kushalpandya/wargo)
published at **https://kushalpandya.github.io/Wargo/**. Sign in with
`demo` / `demo`.

The demo is Wargo's real web app with a fake backend that runs inside the
visitor's browser. [MSW](https://mswjs.io) (Mock Service Worker) answers the
app's API requests and WebSockets with made-up data, so every page works
without a server. Changes you make (stopping a container, editing a file,
reordering apps) are kept in your browser only; "Reset" in the demo banner
forgets them.

Nothing in this branch is part of Wargo itself. The demo code is only ever
added to a throwaway copy of the web UI when the site is built.

## How the site is built

`.github/workflows/pages.yml` on `main` runs for every published release
(or by hand from the Actions tab). It checks out that release, adds
this branch's sources and runs `build.sh`, then publishes the result to
GitHub Pages. Type checking runs against that release's own code, so if the
API changes, the build fails and shows what to update here.

## Layout

| Path | What it is |
|---|---|
| `src/entry.ts` | Starts the fake backend, then loads the real app |
| `src/worker.ts` | Every fake endpoint, in matching order |
| `src/core/` | Helpers: responses, saved state, the events socket, jobs, approvals |
| `src/features/` | One file per area of the app (system, containers, files…) |
| `src/features/host.ts` | The made-up server all pages describe |
| `src/icons/` | App icons from [dashboard-icons](https://github.com/homarr-labs/dashboard-icons) (Apache-2.0, see `src/icons/LICENSE`) |
| `build.sh` | Builds the site from a Wargo checkout |
| `dev.sh` | Runs the demo locally with live reload |
| `vite.demo.config.ts` | Wargo's Vite config, served under `/Wargo/` |

## Working on it

```sh
git clone -b main https://github.com/kushalpandya/wargo wargo
git clone -b gh-pages https://github.com/kushalpandya/wargo wargo-demo
cd wargo-demo && npm ci
sh dev.sh ../wargo          # http://127.0.0.1:5300/Wargo/
```

Requests the demo doesn't fake yet are logged in the browser console as
`[demo] not faked: …` and answer with "isn't available in the demo".

## License

AGPL-3.0-only, like Wargo. The icons in `src/icons/` are Apache-2.0.
