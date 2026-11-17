module replay.local/workbench

go 1.26.0

require github.com/nats-io/nats.go v1.53.1

// Pinned to State A's versions: the offline module cache of the lab image
// holds only these, not the lower minimums nats.go v1.53.1 itself declares.
require (
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)
