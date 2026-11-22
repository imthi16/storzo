# Runbook: storage backup, scrub and restore drill

Status: scripts implemented and exercised once on a dev machine (2026-11-22, worktree
`docs/hybrid-spec-hardening`, compose project `hardening`, image `chrislusf/seaweedfs:4.53_full`).
This is **not** a production-readiness claim: everything below was proven on one host, one disk,
a ~4 MiB dataset, and nothing here is scheduled or installed. Off-host recovery, large-dataset
timings and failure-mode coverage remain unproven.

Companion scripts: `scripts/storage_backup_meta.sh`, `scripts/storage_scrub.py`,
`scripts/storage_restore_drill.sh`. The pre-existing `scripts/restore_integrity.py` covers the
SQLite **audit** volume and is unchanged by this work.

## 0. Which SeaweedFS command, and how it was verified

The pinned image is `chrislusf/seaweedfs:${SEAWEEDFS_TAG:-4.53_full}` (`compose.yaml`).
Verified **inside the running pinned container**, not from docs alone:

- `docker compose -p hardening exec -T storage weed version` → `version 30GB 4.53 4ab66fb76 linux amd64`.
- `printf 'fs.meta.save -h\n' | weed shell` → shows `-o <file>` (gzip if suffix `.gz`), `-obfuscate`, `-v`.
- `printf 'fs.meta.load\n' | weed shell` → `missing a metadata file` (takes the file as a positional argument).
- `weed filer.meta.backup -h` → "continuously backup filer meta data changes … writes to another
  filer store specified in a backup_filer.toml".
- Live probe: `fs.meta.save -o /tmp/meta-probe.save` produced a non-empty export from `weed mini`.

**`weed filer.meta.backup` is a continuous replication command into a second filer store, not a
one-shot file dump.** The backup script therefore uses `weed shell` → `fs.meta.save` (one-shot
export) and the drill restores with `fs.meta.load`. `filer.meta.backup` remains an option for
continuous metadata replication later; it is **not configured or tested** here.

**Pin gotcha (observed):** `scripts/setup_env.py` writes `SEAWEEDFS_TAG=latest` into a fresh `.env`,
which overrides the compose default — a stack started that way ran `latest` (binary 4.48) instead of
4.53. Set `SEAWEEDFS_TAG=4.53_full` in `.env` (as `.env.example` already shows) before trusting any
version-specific behaviour. This run pinned it explicitly.

## 1. `storage_backup_meta.sh` — filer metadata backup

```bash
scripts/storage_backup_meta.sh <backup-dir> [compose-project]   # project defaults to "storzo"
```

Creates `<backup-dir>/filer-meta-<UTC timestamp>/` containing `filer-meta.save`, `info.txt`
(UTC time, project, image, weed version, command) and `SHA256SUMS`, then verifies the manifest
with `sha256sum -c`. Verified live: 6,253-byte export from the demo stack, manifest OK.

**Proves:** the filer metadata export command works on the pinned version; the exported bytes match
their manifest at backup time.
**Does not prove:** consistency under concurrent writes (the export is taken from the **live** filer;
writes during the export may or may not be included), that object data is backed up (it is not),
or that the backup survives host loss (it is written where you point it — choose an off-host target).
For a consistent pair, the drill stops storage **after** the meta export and tars the volume cold;
the volume's embedded filer store is then at least as new as the export.

## 2. `storage_scrub.py` — read-only SHA-256 scrub

```bash
python3 scripts/storage_scrub.py [--endpoint URL] [--env-file .env] \
    [--bucket NAME] [--max-objects N] [--state checkpoint.json]
```

Walks buckets with the master keys (env vars win over `--env-file`), HEADs each object, and for
objects carrying the console's `sha256` upload metadata, streams the body and recomputes SHA-256.
Categories: `match`, `mismatch`, `no-metadata`, `unreadable`. Only LIST/HEAD/GET are issued —
**no S3 writes, ever**. Exit codes: `0` clean, `1` any mismatch, `2` any unreadable (no mismatch),
`3` config error.

- **Bounded:** `--max-objects N` stops after N objects; resume with the same `--state`.
- **Resumable:** the state file keeps a per-bucket `StartAfter` cursor and cumulative counts.
  When a run reaches the **end** of a bucket's listing, that bucket's cursor is cleared, so the
  next run with the same `--state` starts a fresh full pass (safe for scheduled full scrubs).
  Limitation: keys inserted *before* the cursor during a resumed (bounded) scan are skipped
  until the next full pass. A resumed run is not a full re-verification.
- **Metadata key case:** SeaweedFS returns metadata keys as stored. Uploads through the gateway
  arrive with canonicalized headers (`Sha256`); direct-to-storage uploads keep `sha256`. The scrub
  matches the key case-insensitively (bug found and fixed via live testing; regression test added).
- Objects whose ETag changes between HEAD and GET are counted `unreadable`, not silently compared.

**Proves:** for every object with stored sha256 metadata, the bytes read back through the endpoint
match that hash at scrub time. Verified live: a planted wrong-hash object produced `mismatch` and
exit 1; a correct object produced `match`; 52 no-Docker unit tests cover the comparison, pagination,
state, cursor-reset, drill-verification and shell guard paths (`python3 -m pytest scripts/tests -q`).
**Does not prove:** anything about objects **without** stored sha256 — they are counted, not
downloaded, and a scrub over them proves nothing. Console uploads store sha256; SDK/gateway uploads
only if the client sets the metadata. It also does not prove crash safety, absence of bit rot in
unreferenced volume blocks, or SeaweedFS-internal consistency (upstream `volume.scrub` has a known
gap reporting damaged needle headers, seaweedfs issue 11460 — cited from HANDOFF, not re-verified).

## 3. `storage_restore_drill.sh` — restore drill (metadata AND data)

```bash
scripts/storage_restore_drill.sh [source-compose-project]   # defaults to "storzo"
```

Requires a running source stack, docker, host python3 + boto3, and the repo `.env`. The source
project must not be in the reserved `hardening-drill*` namespace. Steps:
seed a scratch bucket (4 objects with sha256 metadata, 1 without) → meta backup (§1) → cold volume
tar (source storage **stopped** during the tar, restarted after; recovery is armed *before* the
stop request, so a partially failed stop still restarts source) → throwaway storage-only stack
under a **unique per-run project** `hardening-drill-<UTC timestamp>-<pid>` on `127.0.0.1:28333`
with fresh credentials (refuses to run if that project or volume name already exists) →
`fs.meta.load` into the empty drill filer and assert the scratch bucket is listed (metadata-only
restore proof) → wipe the **drill's own** volume (only after asserting it differs from the source
volume and carries the drill project prefix), extract the full tar, restart → run the scrub plus
an independent size + SHA-256 check of **every** seeded object, including the no-metadata fixture
(any missing or different key fails the drill) → cleanup (scratch bucket, drill project `down -v`,
temp dir). Only the drill project's volumes are ever deleted; the source volume is mounted
read-only for the tar.

**Measured (this host, 5 objects / ~4.1 MiB, two clean runs, 2026-11-22):**

| Phase | Run 1 | Run 2 |
|---|---|---|
| Meta backup (live export + manifest) | 2 s | 1 s |
| Cold backup (stop + tar + start) | 2 s | 2 s |
| Restore (wipe + extract + start + S3 ready) | 7 s | 7 s |
| Scrub of restored copy | 1 s | 1 s |

Both runs: `DRILL PASS`, scrub `match=4, mismatch=0, no-metadata=1, unreadable=0`.
Failure detection was also demonstrated end-to-end: a deliberately corrupted object left in the
source data was restored by the drill and flagged `mismatch` (non-zero exit) before it was removed.
Re-verified after the isolation/verification hardening (unique drill project, all-object check):
a third run on 2026-11-22 passed with identical timings, and `storage_restore_drill.sh
hardening-drill` was refused live (exit 1) before touching any data.

**RPO (honest):** with this scheme, RPO is "time since the last backup". There is no continuous
replication configured; everything written after the last backup is lost in a restore. The cold
tar additionally **pauses S3 writes** while storage is stopped (2 s here; grows with data size).
**RTO (honest):** ~11 s backup-to-verified on this host for ~4 MiB, excluding detection, decision
and any off-host transfer. It scales with dataset size and was not measured at any realistic size.
Same host, same disk subsystem: this is a restore drill, **not** a disaster-recovery site proof.

## 4. Recovery steps

**A. Full cold restore (data + metadata):**
1. Stop the failed stack's storage (`docker compose -p <project> stop storage`).
2. Create a fresh volume (or empty the target volume — it must be throwaway/empty).
3. Extract the cold tar into it: `docker run --rm -v <vol>:/data -v <backup>:/backup alpine tar xf /backup/storage.tar -C /data`.
4. Start storage with the original credentials (or fresh ones for a loopback-bound copy, as the drill does).
5. Verify: `python3 scripts/storage_scrub.py --endpoint <endpoint>` and spot-check known objects.

**B. Filer metadata only (volume files intact, filer store corrupt):**
1. Copy the backup into the container: `docker compose -p <project> cp <dir>/filer-meta.save storage:/tmp/restore.save`.
2. `printf 'fs.meta.load /tmp/restore.save\n' | docker compose -p <project> exec -T storage weed shell`.
3. List buckets/objects to confirm; then run the scrub. Tested in drill step 5 against an empty
   filer; loading into a *partially corrupt* store was not tested.

**C. Audit trail:** use the pre-existing `scripts/restore_integrity.py` (covers `audit.db`, unchanged).

## 5. Scheduling examples (NOT installed)

cron (daily 02:30 meta backup + weekly scrub Sunday 03:00, off-host target mounted at /mnt/backups):

```cron
30 2 * * *  cd /opt/storzo && scripts/storage_backup_meta.sh /mnt/backups storzo >> /var/log/storzo-backup.log 2>&1
0 3 * * 0   cd /opt/storzo && python3 scripts/storage_scrub.py --state /var/lib/storzo/scrub.json >> /var/log/storzo-scrub.log 2>&1
```

The weekly full scrub may reuse `--state`: a run that reaches the end of the listing clears its
cursor, so each week starts from the beginning (a completed checkpoint no longer causes later
runs to skip everything). `--state` is only *needed* to resume bounded `--max-objects` runs; keys
inserted before the cursor during such a resumed scan are skipped until the next full pass.

systemd timer (`/etc/systemd/system/storzo-backup.service` + `.timer`, illustrative):

```ini
# storzo-backup.service
[Unit]
Description=Storzo filer metadata backup
[Service]
Type=oneshot
WorkingDirectory=/opt/storzo
ExecStart=/opt/storzo/scripts/storage_backup_meta.sh /mnt/backups storzo

# storzo-backup.timer
[Unit]
Description=Daily Storzo filer metadata backup
[Timer]
OnCalendar=*-*-* 02:30:00
Persistent=true
[Install]
WantedBy=timers.target
```

A weekly `storage_restore_drill.sh` on a scratch host is the only way to keep the restore path
honest; it stops source storage briefly, so schedule it in a maintenance window.

## 6. Known gaps

- Backups and drill are same-host; off-host copy, retention and restore from another machine: untested.
- No continuous replication (`filer.meta.backup` exists upstream; not configured or tested here).
- Scrub coverage is limited to objects with stored sha256 metadata (console uploads). SDK uploads
  without that metadata are invisible to verification.
- Cold backup pauses writes; no online/snapshot backup path was tested.
- RPO/RTO measured only at ~4 MiB; no realistic-size or concurrent-load measurement.
- `fs.meta.load` into a partially corrupt filer store: untested.
- Single replica: volume loss between backups loses data; SeaweedFS EC bitrot scrub/repair is
  Enterprise-only (per HANDOFF; not re-verified).
- `setup_env.py` writes `SEAWEEDFS_TAG=latest`, silently overriding the compose pin (see §0).
- Kubernetes/PVC restore path: not covered by these scripts (Compose only).
