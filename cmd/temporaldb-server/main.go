// Command temporaldb-server runs TemporalDB as a standalone server
// (ADR-001 D8): it opens the SQLite store, wires the TQL executor, and
// serves TQL-over-HTTP.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/config"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/graph"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/server"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storage"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/tql"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	events := event.NewStore(db)
	g := graph.NewStore(events, db)

	// search stays nil until internal/vector is wired in (ADR-001 T10);
	// SEARCH then reports a clear "not configured" error rather than the
	// server failing to start.
	var search tql.Searcher
	if cfg.VectorEnabled() {
		log.Print("temporaldb-server: QDRANT_URL/TEI_URL are set, but vector search wiring " +
			"is not implemented yet (ADR-001 T10) - SEARCH will report not-configured")
	}
	executor := tql.NewExecutor(db, events, g, search)

	srv := &http.Server{Addr: cfg.Addr, Handler: server.New(executor)}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("temporaldb-server: listening on %s (data dir %s)", cfg.Addr, cfg.DataDir)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Print("temporaldb-server: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}
