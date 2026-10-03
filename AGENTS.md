# Agents guide

procman runs the processes in `Procfile.dev` for local development and
combines their output. See `README.md` for the surface.

## Writing

Write every word in ASD-STE100 Simplified Technical English (STE):
Markdown docs, code comments, commit messages, and replies in an agent
conversation. See
<https://en.wikipedia.org/wiki/Simplified_Technical_English>.

STE is a controlled English for technical writing: one meaning per
word, one idea per sentence, and the actor named. It is not a house
style. It exists so a reader who is tired, or reading a second
language, or an agent matching on words, all read the same sentence the
same way.

- One idea per sentence. Keep an instruction to 20 words and a
  description to 25.
- Active voice, present tense, and the actor named: say what acts,
  rather than writing "the token is refused".
- One word, one meaning. Keep a term the same everywhere rather than
  varying it for tone.
- Use the simple verb, not a noun made from it: "run the formatter",
  not "perform execution of the formatter".
- Cut what carries nothing: "simply", "just", "note that", "in order
  to".
- Put a warning or a limit before the step it applies to.

Apply it to prose, not to code: an identifier, a command, and a quoted
error message stay as they are.

## Architecture

One package at the repo root. `main.go` holds all of it: parsing the
procfile, starting each process under a pty, the shared fsnotify watcher,
and the restart path. `main_test.go` holds the tests, some of which build
the binary and run it.

## Platform

macOS is the only platform procman is for. It is a local development
tool, it is installed with `go install`, and nothing runs it on a server.
Where a design choice is forced, choose the one that is right on a Mac.

CI is Linux, though, so the tests run on Linux whether or not anybody
does. That is worth keeping rather than skipping the suite off darwin: it
has already caught two bugs that are wrong on both platforms and only
visible on one.

- fsnotify names an event under a watch on `.` as `./x` on inotify and
  `x` on kqueue, so a pattern like `*.rb` matched on a Mac and not on a
  runner. `matchPatterns` cleans the path.
- Reading the master side of a pty returns `EIO` when the child closes
  the slave, which is every process exiting. Linux reports it, macOS does
  not, and procman was printing it as an error.

So a platform difference belongs in the code with a comment saying which
side does what. Do not reach for a build tag or `runtime.GOOS` to make a
test go away.

## Checks

The root `Checkfile` is the list, and CI runs it on every push. Run the
same things before committing:

```sh
go run golang.org/x/tools/cmd/goimports@v0.45.0 -local "$(go list -m)" -w .
go vet ./...
go test -trimpath -buildvcs=false -race -cover ./...
go run golang.org/x/tools/cmd/deadcode@v0.45.0 -test ./...
git ls-files -z '*.go' | xargs -0 gopls check -severity=hint
dprint fmt
git ls-files -z '*.sh' 'bin/*' | xargs -0 shellcheck
```

Install the hook once per clone with `git config core.hooksPath bin`.
The hook runs the Go checks, and only when the push carries a Go file.

## Tests

`TestRestartOnFileChange` starts the real binary, so it waits on output
rather than on the clock. procman prints `watching N dirs` when the watch
is up and `restarting...` when it acts on an event; scan for the line.

A `time.Sleep` before an action the test then asserts on is a guess at
how long a loaded CI box takes, and it fails for the one reason the test
is not looking for: a write that lands before the watch exists produces
no event, and nothing writes again. When a wait times out, print what
procman said. A timeout with no output is a second run to learn anything.

## Commits

- Prefix with what the change acts on: `proc:`, `watch:`, `test:`,
  `doc:`, `ci:`.
- Imperative mood, lowercase except proper nouns. Hard-wrap at 72.
- Include _why_, not just _what_. See `git log` for examples.
- Sign your work with a `Co-Authored-By` trailer.

## Changes

Work happens on a sockeye change: `soc checkout` allocates one and
prints a worktree. After a push, read the checks with `git push && soc
show --wait` rather than sleeping and then reading. A `soc show` that
lands before the push is recorded reports the previous commit's checks,
green, about the wrong code.
