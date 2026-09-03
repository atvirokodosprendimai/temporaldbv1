// Command temporaldb-server runs TemporalDB as a standalone server
// (ADR-001 D8): it opens the SQLite store, wires the TQL executor, starts
// the streaming backup shipper and periodic snapshots (ADR-001 D7), and
// serves TQL-over-HTTP.
package main

import (
	"context"
	"database/sql"
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
	"github.com/atvirokodosprendimai/temporaldbv1/internal/vector"
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

	// search stays nil (SEARCH then reports a clear "not configured"
	// error) unless QDRANT_URL and TEI_URL are both set (ADR-001 D6).
	var search tql.Searcher
	if cfg.VectorEnabled() {
		search = vector.NewIndex(
			vector.NewTEIClient(cfg.TEIURL, cfg.TEIRerankURL),
			vector.NewQdrantClient(cfg.QdrantURL, cfg.QdrantAPIKey),
		)
		log.Printf("temporaldb-server: vector search enabled (qdrant %s, tei %s)", cfg.QdrantURL, cfg.TEIURL)
	}
	sink := backup.NewFileSink(cfg.BackupDir)
	executor := tql.NewExecutor(db, events, g, search, sink)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
	if cfg.Retention > 0 {
		bgWG.Add(1)
		go func() {
			defer bgWG.Done()
			runRetention(ctx, db, executor, cfg.SnapshotInterval, cfg.Retention)
		}()
		log.Printf("temporaldb-server: retention purge enabled (older than %s, checked every %s)", cfg.Retention, cfg.SnapshotInterval)
	}

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

// runRetention purges history older than retention from every collection,
// once per interval — TEMPORALDB_RETENTION's "age-based purge" (README's
// Configuration section). The caller only starts this goroutine when
// retention > 0; unlike runSnapshots there is no immediate first sweep,
// since waiting one interval before the first purge is the safe direction
// (more history retained, not less).
func runRetention(ctx context.Context, db *sql.DB, ex *tql.Executor, interval, retention time.Duration) {
	sweep := func() {
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT collection FROM events`)
		if err != nil {
			log.Printf("temporaldb-server: retention: list collections: %v", err)
			return
		}
		var colls []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				log.Printf("temporaldb-server: retention: scan collection: %v", err)
				return
			}
			colls = append(colls, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			log.Printf("temporaldb-server: retention: list collections: %v", err)
			return
		}

		cutoff := time.Now().Add(-retention)
		for _, c := range colls {
			res, err := ex.Exec(ctx, &tql.PurgeStmt{Collection: c, Before: cutoff})
			if err != nil {
				log.Printf("temporaldb-server: retention: purge %s: %v", c, err)
				continue
			}
			if res.Purged > 0 {
				log.Printf("temporaldb-server: retention: purged %d event(s) from %s before %s",
					res.Purged, c, cutoff.Format(time.RFC3339))
			}
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
