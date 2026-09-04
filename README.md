# procman

`procman` is a process manager for local development on macOS.

Install:

```bash
go install github.com/croaky/procman@latest
```

Define your process definitions in `Procfile.dev`. For example:

```txt
clock: bundle exec ruby cmd/clock.rb
esbuild: bun run buildwatch
queues: bundle exec ruby cmd/queues.rb
web: bundle exec ruby cmd/web.rb
```

Run processes by naming them in a comma-delimited list:

```bash
procman esbuild,web
```

You must list at least one process.
There are no options or flags.

The output from multiple processes will be combined. For example:

```txt
esbuild |
esbuild | > buildwatch
esbuild | > node build.mjs --watch
esbuild |
esbuild | watching...
web     | [67296] Puma starting in cluster mode...
web     | [67296] * Puma version: 6.4.2 (ruby 3.3.0-p0) ("The Eagle of Durango")
web     | [67296] *  Min threads: 12
web     | [67296] *  Max threads: 12
web     | [67296] *  Environment: development
web     | [67296] *   Master PID: 67296
web     | [67296] *      Workers: 2
web     | [67296] *     Restarts: (✔) hot (✖) phased
web     | [67296] * Preloading application
web     | [67296] * Listening on http://0.0.0.0:3000
web     | [67296] Use Ctrl-C to stop
web     | [67296] - Worker 1 (PID: 67331) booted in 0.9s, phase: 0
web     | [67296] - Worker 0 (PID: 67330) booted in 0.9s, phase: 0
```

`procman` will run its processes until it receives a SIGINT (`Ctrl+C`),
`SIGTERM`, or `SIGHUP`.

If a process without a watch annotation finishes, `procman` sends a `SIGINT`
to all remaining running processes, waits 5s, and then sends a `SIGKILL` to
all remaining processes. A process with a watch annotation does not start
this shutdown when it exits.

`procman` runs exactly one process per definition.

It runs the processes in `Procfile.dev` "as-is";
It does not load environment variables from `.env` before running.

## File watching

Add `# watch: PATTERNS` to automatically restart a process when files change:

```txt
web: bundle exec ruby cmd/web.rb  # watch: lib/**/*.rb,ui/**/*.haml
esbuild: bun run buildwatch
```

Patterns are relative to the directory containing `Procfile.dev`.
Glob patterns support `*` (single directory) and `**` (recursive).
Directories matched by `.gitignore` are pruned, so build output doesn't
consume watch descriptors. `.git` and `node_modules` are pruned whether
or not `.gitignore` names them: a project that has stopped building with
npm drops the entry and leaves the tree on disk, and watching it costs
descriptors and, if anything in it is a stale symlink, an error.
Processes without a watch annotation run without file watching.
Changes are debounced (100ms) to avoid rapid restarts.
On change, procman sends SIGINT, waits for the process to exit, then restarts it.
Concurrent restarts are serialized per process to avoid races.

`procman` is distributed via Go source code,
not via a Homebrew package.

`procman` depends on:

- [creack/pty](https://github.com/creack/pty) for PTY interface
- [fsnotify/fsnotify](https://github.com/fsnotify/fsnotify) for file watching
- [bmatcuk/doublestar](https://github.com/bmatcuk/doublestar) for glob patterns

## Developing

Install the pre-push hook once after a clone:

```bash
git config core.hooksPath bin
```

`Checkfile` lists the checks CI runs. `AGENTS.md` gives the commands to
run before a commit, and the commit message rules.

## GitHub repo is a mirror

Development happens on [cibot](https://dancroak.com/cmd/cibot/), a
self-hosted review and CI server, which holds in progress branches.
GitHub receives `main` and the tags so `go install` works.

## License

MIT
