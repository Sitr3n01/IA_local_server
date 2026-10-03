# Browser monitor UI checks

Lint and DOM regression tests for the page that `cia-monitor.exe` serves
(ADR 0019). The page itself is hand-written HTML, CSS and JavaScript embedded
in the Go binary from [`internal/monitor/web`](../internal/monitor/web); this
directory only holds the Node tooling that checks it, so nothing here is built
or shipped.

| Script | What it checks |
|---|---|
| `npm run lint:monitor` | ESLint's recommended rules over the page's scripts, linted as plain ES2022 browser scripts with no build step in between |
| `npm run test:monitor` | The page's behaviour in jsdom: request history kept across a model change or unload, unknown values never invented, and the model actions - a native confirmation that may outlast the request, timeouts that never retry, late or stale replies that cannot restore an old operation |

## Running it

```sh
npm ci
npm run lint:monitor
npm run test:monitor
```

CI runs the same two scripts in the `Monitor UI` job of
[`ci.yml`](../.github/workflows/ci.yml), plus a non-blocking `npm audit` of
this tooling.

## History

This directory used to hold the React and TypeScript operator console that
`cia-console.exe` hosted in WebView2 (ADR 0018). ADR 0019 replaced that
direction with the browser monitor and froze the console; the operator removed
it, its native host and its quality gate on 2026-10-03. The code remains in
the git history before that date.
