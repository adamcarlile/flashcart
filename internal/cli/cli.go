// Package cli parses arguments and wires dependencies. Keeping this out of
// main makes the wiring, and its guardrails, testable.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
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

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           server.New(opts),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(stdout, "flashcart %s listening on %s\n", buildinfo.Version, cfg.Server.Listen)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// selfUpdate is implemented in Task 16.
func selfUpdate(stdout io.Writer) error {
	return errors.New("self-update is not available in this build")
}
