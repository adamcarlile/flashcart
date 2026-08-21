// Package server exposes the HTTP API and the embedded UI.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/drift"
	"github.com/adamcarlile/flashcart/internal/fake"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/plan"
	"github.com/adamcarlile/flashcart/internal/runner"
	"github.com/adamcarlile/flashcart/internal/syncer"
)

// Options are the dependencies the server needs. Provider and Runner are the
// seam: real implementations in production, one fake.Backend in fake mode.
type Options struct {
	Cfg      *config.Config
	Provider nas.Provider
	Runner   runner.Runner
	Free     plan.FreeSpaceFunc
	Fake     *fake.Backend
	Version  string
	Assets   fs.FS
}

// App is the HTTP handler.
type App struct {
	opts   Options
	hub    *Hub
	mux    *http.ServeMux
	ctx    context.Context
	cancel context.CancelFunc

	// wg covers background sync work spawned by handleSync. Shutdown
	// cancels that work but does not itself wait for it to actually
	// finish (its deferred unmount included); wg is what a caller joins
	// to know it truly has, rather than assuming the process's own exit
	// timing happened to be slow enough.
	wg sync.WaitGroup

	mu          sync.Mutex
	busy        bool
	lastSummary *syncer.Summary
	lastSyncAt  time.Time

	// confirmedDrift is the set of drift items shown by the most recent
	// successful /api/plan, keyed by driftKey(item). /api/drift/confirm
	// may only delete items in this set: "explicitly confirmed" means
	// confirmed against something the user was actually shown, not
	// whatever path list the browser happens to post. The server listens
	// on an unauthenticated LAN port and this endpoint permanently
	// destroys data (including game saves), so nothing is deleted on
	// trust alone. The set is replaced wholesale on every plan and
	// cleared after a successful delete, so a stale plan can never
	// authorise a second deletion of paths that no longer exist.
	confirmedDrift map[string]bool
}

// New wires the routes.
func New(o Options) *App {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{opts: o, hub: NewHub(), mux: http.NewServeMux(), ctx: ctx, cancel: cancel}

	a.mux.Handle("GET /", http.FileServer(http.FS(o.Assets)))
	a.mux.HandleFunc("GET /api/status", a.handleStatus)
	a.mux.HandleFunc("GET /api/events", a.hub.serveSSE)
	a.mux.HandleFunc("POST /api/plan", a.handlePlan)
	a.mux.HandleFunc("POST /api/sync", a.handleSync)
	a.mux.HandleFunc("POST /api/drift/confirm", a.handleDriftConfirm)

	// Registered only when the fake backend is present, so the route simply
	// does not exist in production.
	if o.Fake != nil {
		a.mux.HandleFunc("POST /api/fake/scenario", a.handleScenario)
	}
	return a
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.mux.ServeHTTP(w, r) }

// Shutdown cancels the context that background work (currently: an
// in-flight sync) runs on. A sync outlives its originating HTTP request
// deliberately, so it cannot be reached through request cancellation; this
// is the only way to stop one from outside. Cancelling reaches rsync
// itself (runner.Exec ties the child process's lifetime to this context via
// exec.CommandContext) and still lets the deferred unmount in withMounts
// run, because the sync's closure returns promptly once syncer.Run sees
// ctx.Err() rather than being killed out from under it.
//
// Shutdown does not stop the HTTP server; callers are also expected to call
// http.Server.Shutdown. It also force-closes every connected SSE stream, so
// a browser tab left open does not hold http.Server.Shutdown open for its
// full timeout: Shutdown only waits for active handlers to return on their
// own, and a live SSE connection otherwise never does until the client
// disconnects.
//
// Shutdown does not itself wait for the cancelled work to finish; call Wait
// for that.
func (a *App) Shutdown() {
	a.cancel()
	a.hub.closeAll()
}

// Wait blocks until every background sync goroutine started by handleSync
// has returned, including its deferred unmount. Callers should call this
// after Shutdown so process exit is bounded by the work actually
// finishing, not by however long http.Server.Shutdown happened to take.
func (a *App) Wait() {
	a.wg.Wait()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("server: encode response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"err": msg})
}

// acquire enforces single-flight. Plan, sync and drift-confirm cannot
// overlap, and a second browser tab cannot start a parallel run.
func (a *App) acquire() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return false
	}
	a.busy = true
	return true
}

func (a *App) release() {
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()
}

// driftKey identifies a drift item independent of slice ordering.
func driftKey(item plan.DriftItem) string {
	return item.Tree + "\x00" + item.Side + "\x00" + item.Rel
}

// driftSet flattens every tree's drift items from a plan into one set keyed
// by driftKey.
func driftSet(p plan.Plan) map[string]bool {
	set := map[string]bool{}
	for _, tp := range p.Trees {
		for _, d := range tp.Drift {
			set[driftKey(d)] = true
		}
	}
	return set
}

// verifyDrift rejects the whole batch if any item was not shown by the most
// recent plan. If no plan has run since startup, confirmedDrift is nil and
// every confirm is refused: you cannot confirm a deletion you were never
// shown.
//
// Callers must call this only after acquire() has succeeded. Checking
// membership before taking the single-flight lock would reopen the exact
// race this gate exists to close: a concurrent /api/plan could run to
// completion between the check and the lock, replacing confirmedDrift with
// a different set, and the confirm would then delete items validated
// against a plan snapshot that is no longer the most recent.
func (a *App) verifyDrift(items []plan.DriftItem) error {
	a.mu.Lock()
	set := a.confirmedDrift
	a.mu.Unlock()

	for _, it := range items {
		if !set[driftKey(it)] {
			return fmt.Errorf("refusing the whole batch: %s/%s/%q was not shown by the most recent plan", it.Tree, it.Side, it.Rel)
		}
	}
	return nil
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	type resp struct {
		Reachable   bool            `json:"reachable"`
		Err         string          `json:"err,omitempty"`
		NASHost     string          `json:"nasHost"`
		Fake        bool            `json:"fake"`
		Scenario    string          `json:"scenario,omitempty"`
		Scenarios   []string        `json:"scenarios,omitempty"`
		Version     string          `json:"version"`
		Busy        bool            `json:"busy"`
		LastSyncAt  string          `json:"lastSyncAt,omitempty"`
		LastSummary *syncer.Summary `json:"lastSummary,omitempty"`
	}

	out := resp{Version: a.opts.Version, NASHost: a.opts.Cfg.NAS.Host}

	a.mu.Lock()
	out.Busy = a.busy
	out.LastSummary = a.lastSummary
	if !a.lastSyncAt.IsZero() {
		out.LastSyncAt = a.lastSyncAt.UTC().Format(time.RFC3339)
	}
	a.mu.Unlock()

	// Probe mounts nothing, so this stays fast and safe with no network.
	if err := a.opts.Provider.Probe(r.Context()); err != nil {
		out.Err = err.Error()
	} else {
		out.Reachable = true
	}

	if a.opts.Fake != nil {
		out.Fake = true
		out.Scenario = string(a.opts.Fake.Scenario())
		for _, s := range fake.Scenarios {
			out.Scenarios = append(out.Scenarios, string(s))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// withMounts holds the NAS for the duration of fn and always unmounts, even
// on failure. A leaked mount is how the next boot gets slow and confusing.
//
// unmount can genuinely fail ("device or resource busy"), so its error is
// not discarded: it is logged to stderr, which the Batocera service script
// routes to /userdata/system/logs/flashcart.log, giving an operator
// somewhere to find it. It is deliberately not surfaced in the HTTP
// response of an otherwise-successful request: that would put unmount
// noise on the happy path for something the caller cannot act on directly.
func (a *App) withMounts(ctx context.Context, fn func(nas.Mounts) error) error {
	m, unmount, err := a.opts.Provider.Mount(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := unmount(); uerr != nil {
			log.Printf("server: unmount failed: %v", uerr)
		}
	}()
	return fn(m)
}

func (a *App) handlePlan(w http.ResponseWriter, r *http.Request) {
	if !a.acquire() {
		writeErr(w, http.StatusConflict, "a plan or sync is already running")
		return
	}
	defer a.release()

	var out plan.Plan
	err := a.withMounts(r.Context(), func(m nas.Mounts) error {
		p, err := plan.Build(r.Context(), a.opts.Cfg, a.opts.Runner, pass.Passes(a.opts.Cfg, m), a.opts.Free)
		out = p
		return err
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Record what this plan showed the user. /api/drift/confirm may only
	// ever delete items from this set, replacing whatever an earlier plan
	// left behind.
	a.mu.Lock()
	a.confirmedDrift = driftSet(out)
	a.mu.Unlock()

	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	if !a.acquire() {
		writeErr(w, http.StatusConflict, "a plan or sync is already running")
		return
	}

	// The run outlives the request: progress arrives over SSE. It runs on
	// a.ctx rather than the request's context (which dies with the
	// request) or context.Background() (which nothing could ever cancel):
	// a.ctx is what Shutdown cancels on process termination, so the rsync
	// child gets killed and the NFS mount gets released instead of being
	// orphaned by an instant SIGTERM exit.
	//
	// wg is added before the goroutine starts and released when it returns,
	// so a caller that has called Shutdown can also call Wait and know this
	// goroutine, including its deferred unmount, has genuinely finished.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer a.release()
		ctx := a.ctx

		events := make(chan runner.Event, 256)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for e := range events {
				a.hub.broadcast(message{Type: "progress", PassID: e.PassID, Percent: e.Percent})
			}
		}()

		var sum syncer.Summary
		err := a.withMounts(ctx, func(m nas.Mounts) error {
			sum = syncer.Run(ctx, a.opts.Runner, pass.Passes(a.opts.Cfg, m), events)
			return nil
		})
		close(events)
		<-done

		if err != nil {
			sum.OK = false
			sum.Err = err.Error()
		}
		for _, p := range sum.Passes {
			a.hub.broadcast(message{Type: "pass", PassID: p.ID, Label: p.Label, OK: p.OK, Err: p.Err, Warning: p.Warning})
		}

		a.mu.Lock()
		a.lastSummary = &sum
		a.lastSyncAt = time.Now()
		// The world just changed underneath whatever the last /api/plan
		// showed: a completed sync moves files on both sides, so paths
		// confirmedDrift remembers as "shown to the user" may no longer
		// exist, and new drift may now exist that was never shown. Clear
		// it so a stale plan can never authorise a deletion against a
		// tree state that predates this sync — mirroring the same
		// invariant handleDriftConfirm already enforces across a delete.
		a.confirmedDrift = nil
		a.mu.Unlock()

		a.hub.broadcast(message{Type: "done", OK: sum.OK, Err: sum.Err})
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (a *App) handleDriftConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []plan.DriftItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if len(body.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "no items to delete")
		return
	}

	if !a.acquire() {
		writeErr(w, http.StatusConflict, "a plan or sync is already running")
		return
	}
	defer a.release()

	// Every item must have been shown by the most recent plan. Reject the
	// whole batch rather than silently filtering, consistent with
	// drift.Delete's own all-or-nothing contract. This runs only after
	// acquire() has succeeded: checking membership first would let a
	// concurrent /api/plan replace confirmedDrift between the check and the
	// lock, validating this confirm against a plan snapshot that is no
	// longer the most recent.
	if err := a.verifyDrift(body.Items); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var deleted []string
	var deleteErr error
	mountErr := a.withMounts(r.Context(), func(m nas.Mounts) error {
		deleted, deleteErr = drift.Delete(drift.RootsFor(a.opts.Cfg, m), body.Items)
		return nil
	})
	if mountErr != nil {
		writeErr(w, http.StatusServiceUnavailable, mountErr.Error())
		return
	}
	if deleteErr != nil {
		writeErr(w, http.StatusBadRequest, deleteErr.Error())
		return
	}

	// Those items no longer exist: a stale set must not authorise a second
	// deletion.
	a.mu.Lock()
	a.confirmedDrift = nil
	a.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (a *App) handleScenario(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scenario string `json:"scenario"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if err := a.opts.Fake.SetScenario(fake.Scenario(body.Scenario)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"scenario": body.Scenario})
}
