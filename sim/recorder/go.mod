// Isolated module: the recorder is a standalone sim binary built into its own
// image, intentionally separate from the parent module so `go build ./...` at the
// repo root does not pull it in. Stdlib only — no requires.
module github.com/praxis-proxy/dockyard/sim/recorder

go 1.26
