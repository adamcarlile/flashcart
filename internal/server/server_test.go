package server

import (
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
	"github.com/adamcarlile/flashcart/internal/plan"
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
