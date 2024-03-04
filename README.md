# Procman

Procman is process manager for local development on macOS.

Install:

```bash
go install github.com/croaky/procman@latest
```

Define your processes in `Procfile.dev`. For example:

```txt
clock: bundle exec ruby schedule/clock.rb
esbuild: npm run buildwatch
queues: bundle exec ruby queues/poll.rb
web: bundle exec puma -p 3000 -C ./config/puma.rb
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

## Prior art

Procman was
forked from [Hivemind](https://github.com/DarthSim/hivemind) by Sergey Aleksandrovich,
which was inspired by [Foreman](https://github.com/ddollar/foreman) by David Dollar.

Differences between Procman, Hivemind, and Foreman?

- Procman is for macOS only;
  Hivemind and Foreman support Linux.
- Procman expects processes to be defined in `Procfile.dev`;
  Hivemind and Foreman are agnostic about the file name and location.
- Procman runs only one process per process type;
  Foreman supports multiple processes per process type (e.g. `web=1,worker=2`).
- Procman runs the processes in `Procfile.dev` "as-is";
  Hivemind loads environment variables from `.env` before running.
- Procman is distributed via its Go source;
  Hivemind and Foreman can be installed via Homebrew package or Ruby gem.

Why create Procman?

- I had been using `foreman` and the app was feeling sluggish.
  I wanted to keep using a process manager and wondered if something
  purpose-built for my app would feel better.
- I wanted to learn more about testing with Go.
  Given the complexities of dealing with Unix pipes, concurrency, mutexes,
  signal handling, etc., this felt like a worthwhile learning experience.

## Author

Dan Croak (@croaky)

## License

Procman is licensed under the MIT license.

See LICENSE for the full license text.
