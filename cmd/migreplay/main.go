package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/store/migreplay"
)

func main() {
	var (
		dsn     = flag.String("dsn", os.Getenv("MIGREPLAY_DSN"), "throwaway local MySQL *_test DSN (or MIGREPLAY_DSN)")
		baseRef = flag.String("base-ref", "origin/beta", "Git ref whose merge-base with HEAD defines the migration baseline")
		repoDir = flag.String("repo", ".", "path inside the repository")
		timeout = flag.Duration("timeout", 20*time.Minute, "overall replay timeout")
	)
	flag.Parse()
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "migreplay: --dsn or MIGREPLAY_DSN is required")
		os.Exit(2)
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, *timeout)
	defer cancel()
	if err := migreplay.Run(ctx, migreplay.Config{
		DSN:      *dsn,
		BaseRef:  *baseRef,
		RepoDir:  *repoDir,
		Progress: os.Stdout,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "migreplay: FAIL: %v\n", err)
		os.Exit(1)
	}
}
