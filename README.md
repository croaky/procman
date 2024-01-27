# Procman

Procman is a process manager for local development on macOS.

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

Run multiple processes by naming them in a comma-delimited list:

```bash
procman esbuild,web
```

Forked from [Hivemind](https://github.com/DarthSim/hivemind) by Sergey "DarthSim" Aleksandrovich,
which was inspired by [Foreman](https://github.com/ddollar/foreman) by David Dollar.
