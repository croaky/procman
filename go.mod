module github.com/croaky/procman

go 1.27.3

require (
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/creack/pty v1.1.24
	github.com/croaky/is v0.2.0
	github.com/fsnotify/fsnotify v1.9.0
)

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)

tool (
	golang.org/x/tools/cmd/deadcode
	golang.org/x/tools/cmd/goimports
	golang.org/x/vuln/cmd/govulncheck
)
