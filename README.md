# flashcart

Keeps a complete local copy of a Batocera ROM library that normally lives on
an NFS share, so the box works with no network attached.

*A flashcart is the thing you load your library onto at home so it plays
anywhere.*

Sibling to [roadie](https://github.com/adamcarlile/roadie), which does the
equivalent job for an in-car media server.

## How it works

The NAS is no longer mounted at boot. flashcart mounts it only for the
duration of a run, reconciles the two copies, and unmounts. Nothing about
booting depends on the network.

Five rsync passes run in order:

1. BIOS pull
2. ROM content pull
3. ROM metadata pull, `--ignore-existing`
4. ROM metadata push
5. Saves push

Directions differ per file class because two different things share the ROMs
tree. ROM binaries are NAS-owned, since that is where new games are added.
`gamelist.xml` and scraped media are box-owned, because EmulationStation
rewrites them as you play and scrape. Getting this backwards silently
destroys play counts and favourites.

Every pass is additive. Anything present on a destination but absent from its
source is surfaced as drift and deleted only when you tick it and confirm.
`rsync --delete` is never used for a real transfer.

## Constraint

**Scrape on the box, not on the NAS.** Pointing Skraper at the share from a
desktop would have its entries overwritten by the next metadata push.

## Install

On the Batocera box:

```sh
curl -sSL https://raw.githubusercontent.com/adamcarlile/flashcart/main/install.sh | sh
```

Then edit `/userdata/system/flashcart/flashcart.toml` and enable the service
from EmulationStation under Settings, Services, flashcart. The UI is at
`http://<box>:8474`.

## Update

```sh
/userdata/system/flashcart/flashcart update
```

Downloads the latest release, verifies its SHA-256, swaps the binary
atomically and restarts the service.

## Development

Fake mode runs the whole application with no NAS, no Batocera box and no data:

```sh
go run . --fake --config=flashcart.toml.example --listen=:8474 serve
```

Scenarios are switchable live from the UI: `seed`, `steady`, `drift`,
`offline`, `nospace`, `failure`. Only the far side of the `nas.Provider` and
`runner.Runner` seams is scripted, so the server, plan, sync and drift code
being exercised is the real thing.

```sh
go test ./... -race
```

The `internal/pass` integration tests run real rsync over a fixture tree to
prove the filter rules agree with `paths.Classify`.

## Docs

- [Design spec](docs/superpowers/specs/2026-08-20-flashcart-design.md)
- [Implementation plan](docs/superpowers/plans/2026-08-20-flashcart.md)
