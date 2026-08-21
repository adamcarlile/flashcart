package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/fake"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/plan"
	"github.com/adamcarlile/flashcart/internal/server/assets"
)

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>flashcart</html>")},
		"app.js":     &fstest.MapFile{Data: []byte("// app")},
		"style.css":  &fstest.MapFile{Data: []byte("body{}")},
	}
}

func newApp(t *testing.T, scenario fake.Scenario, localRoms string) (*App, *fake.Backend) {
	t.Helper()
	b, err := fake.New(scenario)
	if err != nil {
		t.Fatal(err)
	}
	b.Delay = 0
	cfg := &config.Config{
		NAS: config.NAS{Host: "fake", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: localRoms},
			Bios:  config.Tree{Export: "/e/bios", Local: t.TempDir()},
			Saves: config.Tree{Export: "/e/saves", Local: t.TempDir()},
		},
		SpaceMarginPercent: 10,
	}
	app := New(Options{
		Cfg: cfg, Provider: b, Runner: b, Free: b.FreeSpace,
		Fake: b, Version: "test", Assets: testAssets(),
	})
	return app, b
}

func do(t *testing.T, app *App, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}

func TestStatusReportsReachabilityAndFakeMode(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioSteady, t.TempDir())
	w := do(t, app, http.MethodGet, "/api/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d", w.Code)
	}
	var got struct {
		Reachable bool     `json:"reachable"`
		Fake      bool     `json:"fake"`
		Scenario  string   `json:"scenario"`
		Scenarios []string `json:"scenarios"`
		Version   string   `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Reachable {
		t.Error("Reachable = false in the steady scenario")
	}
	if !got.Fake {
		t.Error("Fake = false while running the fake backend")
	}
	if got.Scenario != string(fake.ScenarioSteady) {
		t.Errorf("Scenario = %q", got.Scenario)
	}
	if len(got.Scenarios) != len(fake.Scenarios) {
		t.Errorf("Scenarios = %v", got.Scenarios)
	}
	if got.Version != "test" {
		t.Errorf("Version = %q", got.Version)
	}
}

func TestStatusReportsOffline(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioOffline, t.TempDir())
	w := do(t, app, http.MethodGet, "/api/status", "")
	var got struct {
		Reachable bool   `json:"reachable"`
		Err       string `json:"err"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Reachable {
		t.Error("Reachable = true in the offline scenario")
	}
	if got.Err == "" {
		t.Error("offline status carries no explanation")
	}
}

func TestPlanReturnsTreesAndDrift(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioDrift, t.TempDir())
	w := do(t, app, http.MethodPost, "/api/plan", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	var got plan.Plan
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Trees) != 3 {
		t.Fatalf("got %d trees, want 3", len(got.Trees))
	}
	total := 0
	for _, tp := range got.Trees {
		total += len(tp.Drift)
	}
	if total == 0 {
		t.Error("drift scenario produced no drift")
	}
}

func TestPlanRefusedWhenOffline(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioOffline, t.TempDir())
	w := do(t, app, http.MethodPost, "/api/plan", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestSyncIsSingleFlight(t *testing.T) {
	app, b := newApp(t, fake.ScenarioSteady, t.TempDir())
	b.Delay = 50 * time.Millisecond // keep the first run in flight

	if w := do(t, app, http.MethodPost, "/api/sync", ""); w.Code != http.StatusAccepted {
		t.Fatalf("first sync status = %d", w.Code)
	}
	if w := do(t, app, http.MethodPost, "/api/sync", ""); w.Code != http.StatusConflict {
		t.Errorf("second sync status = %d, want 409", w.Code)
	}
	if w := do(t, app, http.MethodPost, "/api/plan", ""); w.Code != http.StatusConflict {
		t.Errorf("plan during sync status = %d, want 409", w.Code)
	}
	// verifyDrift is intentionally checked only after acquire() succeeds
	// (see server.go), so a confirm during a sync must be refused by the
	// single-flight lock itself, before drift membership is ever consulted.
	confirmBody := `{"items":[{"tree":"roms","side":"local","rel":"snes/Removed From NAS (USA).zip"}]}`
	if w := do(t, app, http.MethodPost, "/api/drift/confirm", confirmBody); w.Code != http.StatusConflict {
		t.Errorf("drift confirm during sync status = %d, want 409", w.Code)
	}
}

func TestEventsStreamProgress(t *testing.T) {
	app, b := newApp(t, fake.ScenarioSteady, t.TempDir())
	b.Delay = 5 * time.Millisecond

	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	if _, err := http.Post(srv.URL+"/api/sync", "application/json", nil); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	var seen string
	for time.Now().Before(deadline) && !strings.Contains(seen, "progress") {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			seen += string(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(seen, "data: ") || !strings.Contains(seen, "progress") {
		t.Errorf("no progress events received, got: %q", seen)
	}
}

// TestDriftConfirmDeletesLocalPaths exercises the confirm-then-delete path
// end to end. Confirming a deletion now requires that the item was shown in
// the most recent plan, so the test runs /api/plan first and confirms the
// exact drift item the drift scenario actually produces
// ("snes/Removed From NAS (USA).zip" on the local side of the roms tree).
func TestDriftConfirmDeletesLocalPaths(t *testing.T) {
	localRoms := t.TempDir()
	target := filepath.Join(localRoms, "snes", "Removed From NAS (USA).zip")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	if w := do(t, app, http.MethodPost, "/api/plan", ""); w.Code != http.StatusOK {
		t.Fatalf("plan status = %d body = %s", w.Code, w.Body)
	}

	body := `{"items":[{"tree":"roms","side":"local","rel":"snes/Removed From NAS (USA).zip"}]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("confirmed drift path still exists")
	}
}

func TestDriftConfirmRefusesEscapingPaths(t *testing.T) {
	localRoms := t.TempDir()
	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	body := `{"items":[{"tree":"roms","side":"local","rel":"../../etc/passwd"}]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestDriftConfirmRejectsItemNotShownInPlan is the required addition beyond
// the brief: /api/drift/confirm must only ever delete items the most recent
// /api/plan actually showed the user. A path that resolves inside the tree
// root and genuinely exists on disk, but was never part of the plan's drift
// set, must still be refused.
func TestDriftConfirmRejectsItemNotShownInPlan(t *testing.T) {
	localRoms := t.TempDir()
	target := filepath.Join(localRoms, "nes", "Not In Plan.zip")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	if w := do(t, app, http.MethodPost, "/api/plan", ""); w.Code != http.StatusOK {
		t.Fatalf("plan status = %d body = %s", w.Code, w.Body)
	}

	body := `{"items":[{"tree":"roms","side":"local","rel":"nes/Not In Plan.zip"}]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", w.Code, w.Body)
	}
	if _, err := os.Stat(target); err != nil {
		t.Error("item not shown in the plan was deleted anyway")
	}
}

// TestDriftConfirmRejectsMixedBatch proves the gate rejects the whole batch,
// not just the bad item, when a request mixes one item that was in the last
// plan with one that was not. The confirmed item's file must survive: it
// was never deleted, because the bad item in the same request poisoned the
// whole batch, consistent with drift.Delete's own all-or-nothing contract.
func TestDriftConfirmRejectsMixedBatch(t *testing.T) {
	localRoms := t.TempDir()
	planned := filepath.Join(localRoms, "snes", "Removed From NAS (USA).zip")
	unplanned := filepath.Join(localRoms, "nes", "Not In Plan.zip")
	for _, p := range []string{planned, unplanned} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	if w := do(t, app, http.MethodPost, "/api/plan", ""); w.Code != http.StatusOK {
		t.Fatalf("plan status = %d body = %s", w.Code, w.Body)
	}

	body := `{"items":[` +
		`{"tree":"roms","side":"local","rel":"snes/Removed From NAS (USA).zip"},` +
		`{"tree":"roms","side":"local","rel":"nes/Not In Plan.zip"}` +
		`]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", w.Code, w.Body)
	}
	if _, err := os.Stat(planned); err != nil {
		t.Error("the planned item was deleted despite the batch containing an unplanned item")
	}
	if _, err := os.Stat(unplanned); err != nil {
		t.Error("the unplanned item was deleted")
	}
}

// TestDriftConfirmRejectedBeforeAnyPlan verifies that with no plan run since
// startup, the stored drift set is empty and every confirm is refused: you
// cannot confirm a deletion you were never shown.
func TestDriftConfirmRejectedBeforeAnyPlan(t *testing.T) {
	localRoms := t.TempDir()
	target := filepath.Join(localRoms, "snes", "Removed From NAS (USA).zip")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	body := `{"items":[{"tree":"roms","side":"local","rel":"snes/Removed From NAS (USA).zip"}]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", w.Code, w.Body)
	}
	if _, err := os.Stat(target); err != nil {
		t.Error("item was deleted despite no plan ever having run")
	}
}

// TestDriftConfirmClearsSetAfterDelete verifies that a successful delete
// clears the stored drift set, so a stale plan cannot authorise a second
// deletion of items that no longer exist.
func TestDriftConfirmClearsSetAfterDelete(t *testing.T) {
	localRoms := t.TempDir()
	first := filepath.Join(localRoms, "snes", "Removed From NAS (USA).zip")
	second := filepath.Join(localRoms, "ps2", "Old Import (Japan).iso")
	for _, p := range []string{first, second} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	if w := do(t, app, http.MethodPost, "/api/plan", ""); w.Code != http.StatusOK {
		t.Fatalf("plan status = %d body = %s", w.Code, w.Body)
	}

	firstBody := `{"items":[{"tree":"roms","side":"local","rel":"snes/Removed From NAS (USA).zip"}]}`
	if w := do(t, app, http.MethodPost, "/api/drift/confirm", firstBody); w.Code != http.StatusOK {
		t.Fatalf("first confirm status = %d body = %s", w.Code, w.Body)
	}

	// second was also shown by the same plan, but the set was cleared after
	// the first successful delete, so this must now be refused.
	secondBody := `{"items":[{"tree":"roms","side":"local","rel":"ps2/Old Import (Japan).iso"}]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", secondBody)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("second confirm status = %d body = %s, want 400", w.Code, w.Body)
	}
	if _, err := os.Stat(second); err != nil {
		t.Error("item from a stale plan was deleted after the set should have been cleared")
	}
}

func TestScenarioSwitchOnlyExistsInFakeMode(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioSteady, t.TempDir())
	if w := do(t, app, http.MethodPost, "/api/fake/scenario", `{"scenario":"drift"}`); w.Code != http.StatusOK {
		t.Fatalf("scenario switch status = %d body = %s", w.Code, w.Body)
	}
	w := do(t, app, http.MethodGet, "/api/status", "")
	var got struct {
		Scenario string `json:"scenario"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Scenario != "drift" {
		t.Errorf("Scenario = %q after switch", got.Scenario)
	}
}

func TestIndexIsServed(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioSteady, t.TempDir())
	w := do(t, app, http.MethodGet, "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "flashcart") {
		t.Errorf("index body = %q", w.Body.String())
	}
}

func TestEmbeddedAssetsAreComplete(t *testing.T) {
	for _, name := range []string{"index.html", "style.css", "app.js"} {
		b, err := fs.ReadFile(assets.FS, name)
		if err != nil {
			t.Fatalf("embedded asset %s: %v", name, err)
		}
		if len(b) == 0 {
			t.Errorf("embedded asset %s is empty", name)
		}
	}
}

// The UI must never let fake mode pass unnoticed.
func TestIndexCarriesFakeBanner(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fake-bar") {
		t.Error("index.html has no fake mode banner")
	}
}

// MINOR 9: the product's whole thesis is "works with no network," so the
// embedded UI must never request an external font (or anything else): in a
// car, that is a page load that starts with a failing DNS lookup.
func TestIndexMakesNoExternalRequests(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, want := range []string{"fonts.googleapis.com", "fonts.gstatic.com", "http://", "https://"} {
		if strings.Contains(html, want) {
			t.Errorf("index.html references an external resource (%q), which cannot load with no network", want)
		}
	}
}

// MINOR 9: style.css must define its three type roles as system-font
// stacks only, with no @font-face or @import that would pull in a remote
// or embedded font binary.
func TestStylesheetUsesOnlySystemFonts(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, want := range []string{"@font-face", "@import", "fonts.googleapis.com", "fonts.gstatic.com", "data:font", "data:application/font"} {
		if strings.Contains(css, want) {
			t.Errorf("style.css references %q, which requires network access or embeds a font binary", want)
		}
	}
	for _, token := range []string{"--display:", "--ui:", "--mono:"} {
		if !strings.Contains(css, token) {
			t.Errorf("style.css no longer defines %s — the three type roles must stay distinct", token)
		}
	}
}

// Every colour must be reachable in the un-stamped theme state, where only
// prefers-color-scheme applies. A token defined solely inside a media or
// [data-theme] block renders one theme's text on the other theme's ground.
func TestStylesheetDefinesEveryTokenAtRoot(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)

	bare := css[strings.Index(css, ":root {"):strings.Index(css, "@media (prefers-color-scheme: dark)")]
	for _, token := range []string{
		"--bg:", "--surface:", "--surface-2:", "--line:", "--line-soft:",
		"--text:", "--dim:", "--faint:", "--accent:", "--accent-ink:",
		"--accent-soft:", "--ok:", "--ok-soft:", "--warn:", "--warn-soft:",
		"--danger:", "--danger-soft:", "--shadow:",
	} {
		if !strings.Contains(bare, token) {
			t.Errorf("token %s is not defined on bare :root", token)
		}
	}

	// The dark media query must not beat an explicit light choice.
	if !strings.Contains(css, `:root:not([data-theme="light"])`) {
		t.Error("the dark media query is not guarded against an explicit light theme")
	}
	// The toggle must win in the other direction too.
	if !strings.Contains(css, `:root[data-theme="dark"]`) {
		t.Error("there is no explicit dark theme block")
	}
	// A transparent body borrows the host's ground.
	if !strings.Contains(css, "background: var(--bg)") {
		t.Error("body does not paint an explicit background token")
	}
}

// recordingProvider wraps a nas.Provider and records when its unmount
// closure actually runs, so a test can tell a mount was genuinely released
// rather than merely that the request handler returned.
type recordingProvider struct {
	inner     nas.Provider
	unmounted chan struct{}
}

func (p *recordingProvider) Probe(ctx context.Context) error { return p.inner.Probe(ctx) }

func (p *recordingProvider) Mount(ctx context.Context) (nas.Mounts, func() error, error) {
	m, unmount, err := p.inner.Mount(ctx)
	if err != nil {
		return m, unmount, err
	}
	return m, func() error {
		err := unmount()
		close(p.unmounted)
		return err
	}, nil
}

// TestShutdownCancelsInFlightSyncAndReleasesMounts pins the fix for a
// restart-during-sync leak: without a cancellable context reaching the sync
// goroutine, an orphaned rsync keeps running and the NFS mount is held
// until reboot (Task 7's whole reason for existing). Delay is set to a real
// duration so Shutdown genuinely lands mid-run rather than after it has
// already finished.
func TestShutdownCancelsInFlightSyncAndReleasesMounts(t *testing.T) {
	b, err := fake.New(fake.ScenarioSteady)
	if err != nil {
		t.Fatal(err)
	}
	b.Delay = 200 * time.Millisecond

	rp := &recordingProvider{inner: b, unmounted: make(chan struct{})}

	cfg := &config.Config{
		NAS: config.NAS{Host: "fake", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: t.TempDir()},
			Bios:  config.Tree{Export: "/e/bios", Local: t.TempDir()},
			Saves: config.Tree{Export: "/e/saves", Local: t.TempDir()},
		},
		SpaceMarginPercent: 10,
	}
	app := New(Options{
		Cfg: cfg, Provider: rp, Runner: b, Free: b.FreeSpace,
		Fake: b, Version: "test", Assets: testAssets(),
	})

	if w := do(t, app, http.MethodPost, "/api/sync", ""); w.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d", w.Code)
	}

	// Give the background goroutine time to mount and start the first pass
	// before shutting down, so cancellation genuinely lands mid-run.
	time.Sleep(20 * time.Millisecond)

	app.Shutdown()

	select {
	case <-rp.unmounted:
	case <-time.After(2 * time.Second):
		t.Fatal("mounts were never released after Shutdown")
	}

	// Releasing the mount is not enough on its own: confirm the sync
	// actually stopped rather than continuing to run detached.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w := do(t, app, http.MethodGet, "/api/status", "")
		var got struct {
			Busy bool `json:"busy"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("sync did not stop after Shutdown")
}

// IMPORTANT 4: nothing previously joined the goroutine handleSync spawns,
// so a caller could only guess it had finished by polling, or — as cli.go
// used to — by assuming its own timeout happened to be long enough. This
// pins the fix directly: Wait must not return until the background sync,
// including its deferred unmount, has genuinely finished, with no polling
// involved.
func TestWaitBlocksUntilBackgroundSyncGoroutineFinishes(t *testing.T) {
	b, err := fake.New(fake.ScenarioSteady)
	if err != nil {
		t.Fatal(err)
	}
	b.Delay = 100 * time.Millisecond

	rp := &recordingProvider{inner: b, unmounted: make(chan struct{})}

	cfg := &config.Config{
		NAS: config.NAS{Host: "fake", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: t.TempDir()},
			Bios:  config.Tree{Export: "/e/bios", Local: t.TempDir()},
			Saves: config.Tree{Export: "/e/saves", Local: t.TempDir()},
		},
		SpaceMarginPercent: 10,
	}
	app := New(Options{
		Cfg: cfg, Provider: rp, Runner: b, Free: b.FreeSpace,
		Fake: b, Version: "test", Assets: testAssets(),
	})

	if w := do(t, app, http.MethodPost, "/api/sync", ""); w.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d", w.Code)
	}
	time.Sleep(20 * time.Millisecond)

	app.Shutdown()
	app.Wait()

	// Wait already returned: the mount must have been released and busy
	// must already be false, with no polling required to observe it.
	select {
	case <-rp.unmounted:
	default:
		t.Error("Wait returned before the deferred unmount ran")
	}
	w := do(t, app, http.MethodGet, "/api/status", "")
	var got struct {
		Busy bool `json:"busy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Busy {
		t.Error("Wait returned but the sync is still reported busy")
	}
}

// IMPORTANT 4: with a browser's SSE connection left open, http.Server's own
// Shutdown never force-closes an active handler — it only waits for one to
// return on its own — so a live SSE stream previously blocked Shutdown for
// its entire timeout and then returned context.DeadlineExceeded, turning a
// completely normal shutdown into a process exit code of 1. Shutdown now
// force-closes every connected SSE stream, so http.Server.Shutdown must
// succeed well within a short timeout even with a client still attached.
func TestShutdownReleasesSSEConnectionsSoServerShutdownSucceeds(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioSteady, t.TempDir())

	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	app.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("http.Server.Shutdown with a live SSE client attached: %v (want nil: a normal shutdown must not time out and must not exit non-zero)", err)
	}
}

// IMPORTANT 5: a completed sync moves files on both sides, so the drift set
// shown by the last /api/plan can describe paths that no longer exist, or
// miss drift that now does. handleSync must clear confirmedDrift on
// completion, the same way a successful delete already does, so a stale
// pre-sync plan can never authorise a deletion against the post-sync world.
func TestSyncClearsConfirmedDriftSoStalePlanCannotAuthoriseADeletion(t *testing.T) {
	app, b := newApp(t, fake.ScenarioDrift, t.TempDir())
	b.Delay = 0

	pw := do(t, app, http.MethodPost, "/api/plan", "")
	if pw.Code != http.StatusOK {
		t.Fatalf("plan status = %d body = %s", pw.Code, pw.Body)
	}
	var planned plan.Plan
	if err := json.Unmarshal(pw.Body.Bytes(), &planned); err != nil {
		t.Fatal(err)
	}
	var item plan.DriftItem
	for _, tp := range planned.Trees {
		if len(tp.Drift) > 0 {
			item = tp.Drift[0]
			break
		}
	}
	if item.Rel == "" {
		t.Fatal("drift scenario produced no drift to confirm against")
	}

	if w := do(t, app, http.MethodPost, "/api/sync", ""); w.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d", w.Code)
	}
	// b.Delay is 0, but the goroutine still needs to actually run; poll
	// status until busy clears rather than assuming a fixed sleep is enough.
	deadline := time.Now().Add(2 * time.Second)
	for {
		w := do(t, app, http.MethodGet, "/api/status", "")
		var got struct {
			Busy bool `json:"busy"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		if !got.Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sync never completed")
		}
		time.Sleep(5 * time.Millisecond)
	}

	body, _ := json.Marshal(map[string]any{"items": []plan.DriftItem{item}})
	w := do(t, app, http.MethodPost, "/api/drift/confirm", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("drift confirm after a sync status = %d body = %s, want 400: a completed sync must invalidate the pre-sync plan", w.Code, w.Body)
	}
}
