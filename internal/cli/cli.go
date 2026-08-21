// Package cli parses arguments and wires dependencies. Keeping this out of
// main makes the wiring, and its guardrails, testable.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/adamcarlile/flashcart/internal/buildinfo"
	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/fake"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/plan"
	"github.com/adamcarlile/flashcart/internal/runner"
	"github.com/adamcarlile/flashcart/internal/server"
	"github.com/adamcarlile/flashcart/internal/server/assets"
	"github.com/adamcarlile/flashcart/internal/service"
	"github.com/adamcarlile/flashcart/internal/update"
)

// DefaultConfigPath is under /userdata because the Batocera root filesystem
// is a read-only squashfs that is reset on OS update.
const DefaultConfigPath = "/userdata/system/flashcart/flashcart.toml"

// defaultFakeScenario is used when --fake is given without a value.
const defaultFakeScenario = string(fake.ScenarioSteady)

// fakeValue lets "--fake" be a legal flag with no following value, unlike a
// plain flag.String which errors ("flag needs an argument") when nothing
// follows it. Implementing IsBoolFlag makes the flag package treat it like a
// boolean: a bare "--fake" calls Set("true"), while "--fake=drift" calls
// Set("drift") directly. Parse below turns the "true" sentinel into the
// default scenario.
type fakeValue struct{ value string }

func (f *fakeValue) String() string { return f.value }
func (f *fakeValue) Set(s string) error {
	f.value = s
	return nil
}
func (f *fakeValue) IsBoolFlag() bool { return true }

// usage is printed for --help. fs.SetOutput(io.Discard) in Parse silences
// the flag package's own usage printer along with its error text, so this
// is the only usage text a user ever sees.
const usage = `flashcart maintains a local mirror of a ROM library that normally lives on an NFS share.

Usage:
  flashcart [flags] [command]

Commands:
  serve               run the sync server (default)
  version              print the version and exit
  install-service      install and enable the box's service
  uninstall-service    remove the service
  update               self-update to the latest release

Flags:
  --config string   path to flashcart.toml (default "` + DefaultConfigPath + `")
  --listen string   override the configured listen address
  --rsync string    path to the rsync binary (default "rsync")
  --fake[=scenario] run against the scripted fake backend instead of a real NAS
                    (bare --fake picks the "` + defaultFakeScenario + `" scenario)
`

var commands = map[string]bool{
	"serve":             true,
	"version":           true,
	"install-service":   true,
	"uninstall-service": true,
	"update":            true,
}

// Options is the parsed command line.
type Options struct {
	Command     string
	ConfigPath  string
	Fake        string
	Listen      string
	RsyncBinary string
}

// Parse reads arguments. Fake mode is deliberately reachable only from here:
// there is no configuration key for it, so editing a file on the box cannot
// turn it on.
func Parse(args []string) (Options, error) {
	var o Options
	fs := flag.NewFlagSet("flashcart", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.ConfigPath, "config", DefaultConfigPath, "path to flashcart.toml")
	fs.StringVar(&o.Listen, "listen", "", "override the configured listen address")
	fs.StringVar(&o.RsyncBinary, "rsync", "rsync", "path to the rsync binary")
	var fakeFlag fakeValue
	fs.Var(&fakeFlag, "fake", "run against the scripted fake backend (scenario name)")

	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}

	// A bare "--fake" makes the flag package call Set("true"); turn that
	// sentinel into a sensible default scenario rather than a literal
	// scenario named "true".
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "fake" && fakeFlag.value == "true" {
			fakeFlag.value = defaultFakeScenario
		}
	})
	o.Fake = fakeFlag.value

	o.Command = "serve"
	if rest := fs.Args(); len(rest) > 0 {
		if !commands[rest[0]] {
			return Options{}, fmt.Errorf("unknown subcommand %q", rest[0])
		}
		o.Command = rest[0]
	}
	return o, nil
}

// Run executes the command and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	o, err := Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}

	switch o.Command {
	case "version":
		fmt.Fprintln(stdout, buildinfo.Version)
		return 0
	case "install-service":
		if err := service.Install(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "service installed and enabled")
		return 0
	case "uninstall-service":
		if err := service.Uninstall(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "service removed")
		return 0
	case "update":
		if err := selfUpdate(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	if err := serve(o, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func serve(o Options, stdout io.Writer) error {
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return err
	}
	if o.Listen != "" {
		cfg.Server.Listen = o.Listen
	}

	opts := server.Options{
		Cfg:     cfg,
		Version: buildinfo.Version,
		Assets:  assets.FS,
	}

	if o.Fake != "" {
		b, err := fake.New(fake.Scenario(o.Fake))
		if err != nil {
			return err
		}
		opts.Provider, opts.Runner, opts.Free, opts.Fake = b, b, b.FreeSpace, b
		fmt.Fprintf(stdout, "FAKE MODE (%s): nothing will be mounted and no data will move\n", o.Fake)
	} else {
		opts.Provider = nas.NewNFS(cfg)
		opts.Runner = runner.NewExec(o.RsyncBinary)
		opts.Free = plan.FreeSpace
	}

	app := server.New(opts)
	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Without this, Go terminates on SIGTERM immediately: the rsync child
	// of an in-flight sync is never signalled (it keeps writing, detached
	// from any supervisor), the NFS mount is never released (held until
	// reboot, exactly the leak withMounts exists to prevent), and the
	// restarted process has no idea an orphaned rsync is still running, so
	// a fresh sync could run a second rsync over the same tree. An
	// ordinary "flashcart update" reaches this path via a service restart.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Fprintf(stdout, "flashcart %s listening on %s\n", buildinfo.Version, cfg.Server.Listen)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		fmt.Fprintln(stdout, "flashcart: shutting down")
		// Cancel background work (an in-flight sync) first, so its rsync
		// child is killed and its deferred unmount runs, before the HTTP
		// server stops accepting the SSE/status requests that surface
		// that shutdown to a browser. Shutdown also force-closes any live
		// SSE stream, so a browser tab left open does not hold
		// srv.Shutdown blocked for its full timeout below.
		app.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		// Join the sync goroutine before returning, so process exit is
		// bounded by that work (including its deferred unmount) actually
		// finishing, rather than by srv.Shutdown's timing happening to
		// have been slow enough to cover it.
		app.Wait()
		return err
	}
}

// Repo is the GitHub repository self-update reads releases from.
const Repo = "adamcarlile/flashcart"

func selfUpdate(stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rel, err := update.Latest(ctx, Repo)
	if err != nil {
		return err
	}
	if strings.TrimPrefix(rel.Tag, "v") == strings.TrimPrefix(buildinfo.Version, "v") {
		fmt.Fprintf(stdout, "already on %s\n", buildinfo.Version)
		return nil
	}

	asset := fmt.Sprintf("flashcart_%s_%s", runtime.GOOS, runtime.GOARCH)
	binURL, ok := rel.Assets[asset]
	if !ok {
		return fmt.Errorf("release %s has no asset %q", rel.Tag, asset)
	}
	sumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt", rel.Tag)
	}

	fmt.Fprintf(stdout, "downloading %s (%s)\n", asset, rel.Tag)
	sumsBody, err := update.Fetch(ctx, sumsURL)
	if err != nil {
		return err
	}
	payload, err := update.Fetch(ctx, binURL)
	if err != nil {
		return err
	}

	sums := update.ParseChecksums(string(sumsBody))
	want, ok := sums[asset]
	if !ok {
		return fmt.Errorf("checksums.txt has no entry for %q", asset)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := update.VerifyAndSwap(self, payload, want); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "updated to %s\n", rel.Tag)

	var attempted bool
	var restartErr error
	if _, err := exec.LookPath("batocera-services"); err == nil {
		attempted = true
		restartErr = exec.Command("batocera-services", "restart", service.Name).Run()
	}
	return reportRestart(stdout, attempted, restartErr)
}

// reportRestart writes the outcome of restarting the service after a
// self-update and decides whether the overall update command succeeded. A
// restart that was never attempted (batocera-services is absent, e.g. a
// development machine) is not an error: there is nothing to restart.
//
// A restart that was attempted and failed is: the binary on disk is already
// the new one, but the running process is still the old one, so a caller
// that only saw "updated to vX.Y.Z" would wrongly believe the new code is
// live. This is the same class of bug fixed in Task 15, where the service
// script itself reported success on a failed restart; swallowing the error
// here would let it back in through the Go caller instead. The command
// exits non-zero because the operator asked for an update and only got
// half of one: the binary is staged, but nothing is running it yet.
func reportRestart(stdout io.Writer, attempted bool, restartErr error) error {
	if restartErr != nil {
		fmt.Fprintf(stdout, "service restart failed: %v\n", restartErr)
		fmt.Fprintln(stdout, "the new binary is installed, but the service is still running the old one; it will take effect on the next manual start or reboot")
		return fmt.Errorf("update installed but service restart failed: %w", restartErr)
	}
	if attempted {
		fmt.Fprintln(stdout, "service restarted")
	}
	return nil
}
