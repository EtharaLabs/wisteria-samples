// Command replay starts a cluster from a server binary, drives one named
// timeline, records it, and prints the verdict of named checks.
//
// It exits 0 whenever the timeline ran, whatever the verdict, 2 on an
// infrastructure failure, and 64 on a usage error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"replay.local/workbench/replayer"
)

func main() {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	server := fs.String("server", "", "path to the nats-server binary under test")
	timeline := fs.String("timeline", "stale-after-reset", "timeline: "+strings.Join(replayer.Timelines, ", "))
	nodes := fs.Int("nodes", 3, "cluster size, 1 or 3")
	seed := fs.Uint64("seed", 1, "seed for payload content")
	out := fs.String("out", "", "output directory for recording.jsonl and verdict.json")
	load := fs.Int("load", 0, "busy goroutines raising CPU load during the run, at most the CPU count")
	keep := fs.Bool("keep", false, "keep the cluster's temp root")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(64)
	}
	if *server == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "replay: -server and -out are required")
		fs.Usage()
		os.Exit(64)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	v, err := replayer.Run(ctx, replayer.Config{
		Server: *server, Timeline: *timeline, Nodes: *nodes, Seed: *seed, Out: *out, Load: *load, KeepRoot: *keep,
	})
	if err != nil {
		// Run returns an error only when the timeline could not run at all.
		var infra *replayer.InfraError
		if !errors.As(err, &infra) {
			err = &replayer.InfraError{Err: err}
		}
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(2)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
