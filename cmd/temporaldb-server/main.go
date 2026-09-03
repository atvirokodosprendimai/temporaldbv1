// Command temporaldb-server runs TemporalDB as a standalone server
// (ADR-001 D8): it opens the SQLite store, wires the TQL executor, starts
// the streaming backup shipper and periodic snapshots (ADR-001 D7), and
// serves TQL-over-HTTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/backup"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sink := backup.NewFileSink(cfg.BackupDir)
	lastShipped, err := backup.LastShippedSeq(cfg.BackupDir)
	if err != nil {
		return fmt.Errorf("temporaldb-server: resume shipper: %w", err)
	}
	shipper := backup.NewShipper(events, sink, cfg.BackupInterval, lastShipped)
	snapshotter := backup.NewSnapshotter(db, filepath.Join(cfg.DataDir, ".snapshot-tmp"), sink)

	var bgWG sync.WaitGroup
	bgWG.Add(2)
	go func() {
		defer bgWG.Done()
		if err := shipper.Run(ctx); err != nil {
			log.Printf("temporaldb-server: backup shipper stopped: %v", err)
		}
	}()
	go func() {
		defer bgWG.Done()
		runSnapshots(ctx, snapshotter, cfg.SnapshotInterval)
	}()

	srv := &http.Server{Addr: cfg.Addr, Handler: server.New(executor)}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("temporaldb-server: listening on %s (data dir %s, backup dir %s)",
			cfg.Addr, cfg.DataDir, cfg.BackupDir)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		stop() // also releases the backup goroutines
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			bgWG.Wait()
			return err
		}
	case <-ctx.Done():
		log.Print("temporaldb-server: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			bgWG.Wait()
			return err
		}
	}
	bgWG.Wait()
	return nil
}

// runSnapshots takes one snapshot immediately — establishing a restorable
// baseline as soon as the server starts, rather than waiting a full
// interval — then one more per interval until ctx is cancelled.
func runSnapshots(ctx context.Context, snap *backup.Snapshotter, interval time.Duration) {
	take := func() {
		if _, err := snap.Snapshot(ctx); err != nil {
			log.Printf("temporaldb-server: snapshot failed: %v", err)
		}
	}
	take()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			take()
		}
	}
}
