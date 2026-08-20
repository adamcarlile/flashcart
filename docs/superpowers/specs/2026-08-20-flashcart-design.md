# flashcart design

Date: 2026-08-20
Status: approved, ready for implementation planning

## Problem

The Batocera games box mounts its ROMs, BIOS and saves from the NAS over NFS at boot.
It therefore only works at home. Taking it on the road requires a local copy of the
library and somewhere for saves to go that is not the NAS.

flashcart is a small Go web application, running on the Batocera box, that maintains
that local copy and reconciles it with the NAS on demand. Named for the cartridge you
load your library onto at home so it plays anywhere with nothing else attached, which
is the product in one sentence.

Sibling in spirit to [roadie](https://github.com/adamcarlile/roadie), which does the
equivalent job for the in-car media server, but a separate tool. See "Why not extend
roadie".

## Current state, as measured 2026-08-20

Box: `root@10.132.1.151`, Batocera v42 (2025/10/06), hostname `BATOCERA`.
NAS: `10.132.1.25`, Synology, exports under `/volume2/retrogaming`.

`/boot/batocera-boot.conf`:

```
sharedevice=NETWORK
sharenetwork_nfs0=ROMS@10.132.1.25:/volume2/retrogaming/roms
sharenetwork_nfs1=BIOS@10.132.1.25:/volume2/retrogaming/bios
sharenetwork_nfs2=SAVES@10.132.1.25:/volume2/retrogaming/saves
```

Storage: `/userdata` is local ext4 on `/dev/nvme0n1p2`, 459 G total, 2.9 G used,
**433 G free**. Only `roms`, `bios` and `saves` are NFS. EmulationStation config,
themes, decorations and screenshots are already local.

The three NFS mounts are `hard`, meaning a boot out of range hangs indefinitely.

Library size:

| Tree | Size |
| --- | --- |
| roms/ps3 | 34.0 G |
| roms/ps2 | 20.8 G |
| roms/xbox | 9.0 G |
| roms/xbox360 | 7.3 G |
| roms/gamecube | 6.2 G |
| roms/psx | 4.3 G |
| roms/wii | 4.1 G |
| roms/neogeo | 2.4 G |
| roms/dreamcast | 2.2 G |
| all 223 other systems combined | 1.3 G |
| **roms total** | **91.6 G** |
| bios | 0.6 G |
| saves | 0.6 G |
| **total** | **~93 G** |

232 system directories exist, 24 have a `gamelist.xml`, 9 account for 99% of the bytes.

Metadata observed in the roms tree: `images` (23 systems), `videos` (3), `manuals` (1),
`gamelist.xml` (24), plus 198 static `_info.txt` files and 5 Synology `@eaDir`
directories.

`/userdata/roms/snes/gamelist.xml` contains live `<playcount>` values and was last
written 2026-02-01, confirming EmulationStation mutates gamelists in place on the NAS
as games are played. Scraped media is Batocera-scraper-shaped (`-image.png`,
`-marquee.png`, `-thumb.png`).

rsync 3.3.0 is present at `/usr/bin/rsync`. `batocera-services` exists;
`/userdata/system/services` does not yet exist and will be created.

## Decisions

1. **Mirror the entire library locally and drop NFS at runtime.** 93 G into 433 G free
   makes subset selection machinery for a constraint that does not exist. Local NVMe
   also outperforms NFS for the disc-based systems.
2. **The NAS becomes an archive synced on demand, not a live dependency.** flashcart
   mounts it, syncs, and unmounts. Nothing at boot touches the network.
3. **Manual trigger from a small web UI**, not automatic sync.
4. **The box is the sole writer of saves** (confirmed with the user), so saves need no
   merge logic.

## Sync model

### Three trees, three rules

| Tree | NAS path | Local path | Direction |
| --- | --- | --- | --- |
| bios | `/volume2/retrogaming/bios` | `/userdata/bios` | pull, NAS wins |
| saves | `/volume2/retrogaming/saves` | `/userdata/saves` | push, box wins |
| roms | `/volume2/retrogaming/roms` | `/userdata/roms` | split, see below |

### The roms split

Two different classes of file share the roms tree and must move in opposite directions.

| Class | Paths | Direction |
| --- | --- | --- |
| Metadata | `/<system>/gamelist.xml`, `/<system>/images/`, `/<system>/videos/`, `/<system>/manuals/` | push, box wins |
| Content | everything else | pull, NAS wins |
| Ignored | `@eaDir` anywhere, `.flashcart-partial` | excluded both ways |

Metadata is box-owned because EmulationStation rewrites `gamelist.xml` on exit to
record play counts, last-played and favourites, and because scraping happens on the
box. Content is NAS-owned because that is where new games are added.

**Filter rules must be anchored to `/<system>/<dir>/`, not matched at any depth.**
The roms tree contains game directories at the same depth as metadata directories,
including `God of War Collection.ps3`, `Skate 3.ps3`, `Tiger Woods PGA Tour 14.ps3`,
`Prince of Persia`, `Wolfenstein 3D`, `Doom`, and content directories `main`, `data`,
`mame2003`, `fbneo`, `pygun`, `retrotrivia`, `game-musics`. An unanchored
`--exclude=images/` would misclassify content as metadata. This is the single most
likely source of a real bug and is tested directly.

### Seeding requires no special mode

Metadata syncs as two passes:

1. pull with `--ignore-existing`, bringing down anything the box has never seen
2. push normally, so the box's version wins wherever it has one

On first run the box is empty, so everything arrives. In steady state, local play
counts and favourites always win. One code path covers both.

### Five passes, in order

1. bios pull
2. roms content pull, excluding metadata and ignored paths
3. roms metadata pull with `--ignore-existing`
4. roms metadata push
5. saves push

### Deletion is never implicit

Every pass runs additive. Anything present on the destination but absent from the
source is surfaced as drift and removed only on explicit per-item confirmation.

**Drift is computed against projected state, not current state.** Passes run in order,
and a dry run copies nothing, so a naive drift calculation on a first run would report
every NAS-side `gamelist.xml` and all scraped media as drift during pass 4, because
pass 3's pull has not yet happened. Drift for pass N is therefore computed against the
state projected after passes 1 to N-1 in the same run: paths an earlier pass would
create are subtracted before drift is reported. On a seed run this correctly yields
empty drift lists.

Drift deletion does **not** use `rsync --delete`. Because the NAS is mounted during
the operation, both sides are ordinary filesystem paths, so flashcart removes exactly
the confirmed paths and nothing else. No filter expression sits between the user's
intent and an irreversible delete. A mis-scoped `--delete` against `/userdata/saves`
is the worst outcome this design can produce, so the mechanism is removed rather than
guarded.

### Constraint this creates

**Scraping must happen on the box, not on the NAS.** Pointing Skraper or similar at
`/volume2/retrogaming/roms` from a desktop would have its entries overwritten by the
next metadata push. This matches current practice (all media is Batocera-scraper
output) but becomes a documented rule.

## Architecture

### Packages

| Package | Responsibility |
| --- | --- |
| `config` | TOML: NAS host, the three export and local path pairs, listen port. Validated on load |
| `nas` | Reachability probe, mount and unmount lifecycle |
| `plan` | Dry-run every pass, parse `--itemize-changes` into changesets and drift lists |
| `sync` | Execute passes for real, stream progress |
| `drift` | Delete explicitly confirmed paths, and nothing else |
| `server` | HTTP, SSE, embedded vanilla-JS UI |
| `service` | `batocera-services` install and uninstall |
| `buildinfo` | Version and checksum-verified self-update, ported from roadie |

### Mount lifecycle

flashcart owns the mounts entirely. They exist only for the duration of a plan or
sync, under `/var/run/flashcart/nas/{roms,bios,saves}`.

- bios mounted read-only, roms and saves read-write
- `soft,timeo=50,retrans=2,proto=tcp,vers=4.0`, so a NAS that vanishes mid-sync
  produces a readable error rather than a wedged process
- unmount is deferred and always runs, including on failure or panic

### Data flow

`GET /api/status` performs a one second TCP probe of `10.132.1.25:2049` and returns
reachability plus the last sync summary. It mounts nothing, so the page is instant and
safe to open in the car.

**Plan** mounts, runs all five passes with `-n --itemize-changes`, parses, unmounts,
and returns per-tree counts and byte totals plus drift lists.

**Sync** does the same with `--info=progress2`, streaming to the browser over SSE.

**Confirm drift** is a separate endpoint taking an explicit path list.

### Process execution

No shell interpolation, ever. Every rsync invocation goes through `exec.Command` with
an argument slice. The library contains filenames with ampersands, apostrophes,
brackets, commas and spaces, for example `Adventures of Batman & Robin, The (USA).zip`.
This is a correctness requirement.

## UI

Single page, no framework, no build step, embedded in the binary.

- Header: NAS status pill, last sync time, version
- Three cards (ROMs, BIOS, Saves), each showing incoming and outgoing counts and bytes
- Plan and Sync buttons, single-flight so they cannot overlap
- Live progress log with a per-pass bar
- Collapsed drift panel with per-item checkboxes and a separate confirm button

## Deployment

- Binary and config at `/userdata/system/flashcart/`
- Service script at `/userdata/system/services/flashcart`, making it toggleable from
  EmulationStation under Settings, Services
- Everything under `/userdata`, which persists across Batocera OS updates. The root
  filesystem is a read-only squashfs with an overlay and is reset on update
- Listens on port 8474, leaving 8473 free for roadie
- Releases via GoReleaser on git tag, self-update verifies SHA-256 before swapping,
  both ported from roadie
- Built `CGO_ENABLED=0` for a static binary

## Rollout

The boot config is edited last. Until that point every step is reversible by rebooting.

| Phase | Action | Rollback |
| --- | --- | --- |
| 0 | Build and test against temp directories. Box not involved | n/a |
| 1 | Install binary, config and service. Boot behaviour unchanged | Delete the directory |
| 2 | From SSH, `umount /userdata/{roms,bios,saves}` to expose the empty local dirs. Run first seed, ~93 G | Reboot, NFS returns as before |
| 3 | Verify: systems populate in ES, gamelists retain play counts, a save loads, a PS2 game boots | Reboot |
| 4 | Edit `/boot/batocera-boot.conf`: remount rw, comment the three `sharenetwork_nfs*` lines, set `sharedevice=INTERNAL`, remount ro, reboot | Uncomment, reboot |
| 5 | Pull the network cable, confirm it boots to a full library. Then a real trip | As above |

Phase 4's rollback is non-destructive in both directions: re-enabling the NFS lines
shadows the local copy rather than deleting it.

First seed is expected to take 30 to 60 minutes. The 34 G of PS3 will stream near line
rate; roughly half a million small scraper PNGs will not.

## Error handling

- **NAS unreachable**: status pill states it, sync disabled, no retry loop
- **Insufficient space**: the plan totals required bytes and refuses if it exceeds free
  space less a 10% margin
- **Interrupted transfer**: `--partial --partial-dir=.flashcart-partial`, so a sync cut
  short resumes rather than restarting a 30 G file. The partial dir is excluded both ways
- **Single-flight**: mutex plus lock file, so Plan and Sync cannot overlap and a second
  browser tab cannot start a parallel run
- **Unmount always happens**, deferred, including on failure or panic
- **Mount failures are distinguished**: unreachable host, missing export and permission
  denied produce different messages, because they need different fixes
- **rsync non-zero exit**: stderr captured and surfaced verbatim, the pass marked
  failed, remaining passes abandoned, unmount still runs
- **`@eaDir` excluded both directions**, so the Synology indexer never manufactures
  phantom drift

## Accepted risks

- If EmulationStation rewrites `gamelist.xml` during a metadata push, the NAS can
  receive a half-written file. rsync's write-then-rename keeps the NAS side atomic, and
  the next sync corrects it. Acceptable because the NAS copy is a backup, not the source
  of truth.
- The local mirror has a ceiling of 433 G. At 93 G today there is ample headroom, but a
  sustained PS3 or Wii U habit would eventually reach it. The space precheck fails
  safely rather than filling the disk, and subset selection can be added later if needed.

## Non-goals

- Detecting a running emulator and refusing to sync. The trigger is manual, so timing
  is the user's call.
- Subset or per-title selection. Deliberately deferred, since the whole library fits.
- Bidirectional save merge. The box is the sole writer.
- Scraping, metadata editing or anything EmulationStation already does.

## Why not extend roadie

roadie and flashcart share patterns but little else. Different init systems
(systemd against `batocera-services`), different platforms (Ubuntu against buildroot),
different filesystem tuning (exfat against ext4), and different UI semantics (pick
titles from an oversized library against mirror everything). roadie's manifest and
picker, the bulk of its value, do not apply here. Sharing a repo would couple the
carnet's release cadence to the games box for maybe 30% common code.

## Testing

No test requires the NAS or the Batocera box.

**Filter classification tests** carry the highest value, using a fixture of real paths
captured from the box:

```
snes/gamelist.xml                              -> push
snes/images/ActRaiser (USA)-image.png          -> push
snes/ActRaiser (USA).zip                       -> pull
ps3/God of War Collection.ps3/USRDIR/...       -> pull    (game dir, not metadata)
ports/main/...                                 -> pull
mame/mame2003/...                              -> pull
snes/_info.txt                                 -> pull
@eaDir/...                                     -> excluded
```

Alongside those:

- `config` load and validation table tests, following roadie's precedent
- `plan` parser tests against captured real `--itemize-changes` output
- an integration test using two temp directories as fake NAS and local with the real
  rsync binary, asserting:
  - seed populates everything including metadata
  - a locally modified `gamelist.xml` survives a sync unchanged
  - a NAS-side new ROM arrives locally
  - a locally deleted ROM is reported as drift and is **not** deleted from the NAS
  - confirmed drift deletes exactly the named path and nothing adjacent
  - **a seed run against an empty local tree reports zero drift**, verifying the
    projected-state calculation
