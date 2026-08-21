# Cutover runbook

What was actually done to the box, what was observed, and how to undo it.
Written 2026-08-21, the day of the cutover.

The box: Batocera v42 mini PC at `10.132.1.151`, previously loading its
library over NFS from a Synology at `10.132.1.25`
(`/volume2/retrogaming/{roms,bios,saves}`).

## Result

The box boots entirely from `/dev/nvme0n1p2`. No NFS mount exists at any
point in the boot path. The NAS is mounted only for the duration of a
flashcart run.

```
/userdata          459 G total, 96 G used, 340 G free (22%)
/userdata/roms      92 G, 232 directories, 26 gamelists, 19 with games
/userdata/bios     576 M
/userdata/saves    654 M, 131 files
```

Play counts survived: 40 `<playcount>` entries across 14 systems.

## What was run

Steps 1 and 2 (install, confirm the UI sees the NAS) were done on
2026-08-20. Steps 3 to 7 on 2026-08-21.

**Step 3, expose the local disk.** Stopped EmulationStation, then
`umount /userdata/roms /userdata/bios /userdata/saves`. This is the step
that makes flashcart's numbers mean anything: before it, local and remote
resolve to the same directory.

**Step 4, plan and seed.** The plan, in full, as it read before the seed:

| Pass | Dir | Files | Bytes |
|---|---|---:|---:|
| `bios-pull` | in | 103 | 522,957,568 |
| `roms-content-pull` | in | 3,126 | 97,691,079,810 |
| `roms-metadata-pull` | in | 2,791 | 592,640,375 |
| `roms-metadata-push` | out | 61 | 26,110,310 |
| `saves-pull` | in | 129 | 684,000,084 |
| `saves-push` | out | 2 | 115 |

`required = 99,490,677,837`, `free = 464,157,618,176`, `sufficient = true`.

All six passes completed ok, finishing at 09:28:51 UTC. The precise start
was not recorded; a sample partway through showed 22 GB of ROMs copied with
BIOS already complete. Budget an hour.

**Step 5, verify.** A second plan found nothing left to move in either
direction across all six passes, and reported `free = 364.7 GB` rather than
the NAS's 4.3 TB, which independently confirms the trees are local.

**Step 6, boot config.** Backed up to
`/boot/batocera-boot.conf.pre-flashcart`, then set `sharedevice=INTERNAL`
and commented all three `sharenetwork_nfs*` lines. `/boot` remounted `ro`
afterwards.

**Step 7, reboot.** Back in about 40 seconds. Zero NFS mounts, library
intact, flashcart started itself.

## What differed from the plan

**The install enabled the service without starting it.** `batocera-services`
has `enable` and `start` as separate verbs, and v0.1.0 called only the
first. The install reported success and printed a URL that answered nothing
until a reboot. Fixed in v0.1.1: install now starts the service too, and the
installer polls the port before promising a URL. The absent log file was the
giveaway, since the service script creates it on its first line.

**The first plan reported 584 drifted paths, not zero.** The runbook said to
stop and fix the projected-state calculation. That was the wrong diagnosis.
Batocera keeps a skeleton at `/usr/share/batocera/datainit` and
`/etc/init.d/S12populateshare` copies it into `/userdata` on every boot: 25
`_info.txt` files, 8 bundled homebrew ROMs, and 551 BIOS support files (the
bluemsx `Machines` set, FBNeo and MAME dat files, `NstDatabase.xml`). The
NFS mounts had been sitting on top of it since the box was built, so none of
it had ever been visible.

All 584 were verified present under `datainit`, byte-identical and with
matching timestamps. Deleting them would have been pointless where
`S12populateshare` restores them on the next boot, and harmful in `bios`,
where that data is what those emulators load. Fixed in v0.1.2, which
excludes local paths matching the factory tree and reports the count instead:
`drift = 0, factoryExcluded = 584`.

**The `snes` play-count check was a bad canary.** The runbook said to expect
`grep -c "<playcount>" /userdata/roms/snes/gamelist.xml` to be non-zero. The
snes gamelist is a single-game file with no play counts in it at all, so the
check reads zero on a perfectly good copy. Check the systems actually played
instead:

```sh
for f in /userdata/roms/*/gamelist.xml; do
  n=$(grep -c "<playcount>" "$f"); [ "$n" -gt 0 ] && echo "$n $f"
done
```

**A plan run before step 3 reads all zeros.** Expected, not a fault. While
the NFS mounts are still in place, every pass compares a directory against
itself. The `free` figure is the tell: 4.3 TB is the NAS, 433 GB is the local
disk.

## Rollback

The local library is shadowed, never deleted, so this is reversible in both
directions with nothing lost either way.

```sh
mount -o remount,rw /boot
cp /boot/batocera-boot.conf.pre-flashcart /boot/batocera-boot.conf
mount -o remount,ro /boot
reboot
```

The box comes back on NFS exactly as before, with the 92 GB local copy still
on disk underneath, ready to be exposed again by unmounting.

## Operating it from here

Sync from `http://10.132.1.151:8474`, manually, when the box is home and on
the network. The box is the only writer of saves and the only place scraping
may happen: pointing Skraper at the share from a desktop would have its
entries overwritten by the next metadata push.

Drift should stay at zero. Anything that appears there is a real divergence
worth reading before ticking.
