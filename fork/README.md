# Magpie fork release and dev updates

This directory keeps the answer-timeout patch on current upstream Magpie
without letting Magpie's official updater replace it. The scheduled workflow
merges upstream into the patch branch in a disposable runner, tests that tree, and publishes
an immutable Linux AMD64 release. It also publishes the tested binary as a
public OCI artifact at `ghcr.io/ryan-zhao-go/magpie-fork:latest` for hosts that
cannot reach GitHub's Release web endpoint. It never force-pushes the patch
branch.

Merging preserves earlier conflict resolutions and the published patch history.
A new conflict still stops the run before testing or publishing; resolve it in
the patch branch rather than skipping the failing command.

Manual runs check out the selected workflow branch. Set `verify_only=true` to
run the release gates, including Chromium and WebKit, without creating tags,
releases or OCI artifacts. `upstream_ref` accepts an upstream ref or commit SHA;
use the same SHA for verification and release when an exact source is required.
Scheduled runs continue to use `feature/answer-timeout` and current upstream
`main`. The gateway gate covers both the upstream group budget and the fork's
answer-timeout and reasoning-recovery regressions. It also checks upstream's
trusted-thinking handover alongside whole-summary byte-cap recovery, so a
conflict resolution must retain both behaviors.

A release is created as a draft and published only when all five assets are
uploaded. A retry preserves an existing release's version and binary, uploads
only missing draft assets, and refuses a different source identity. An existing
complete release skips rebuilding but does not skip GHCR repair: its original
binary is downloaded and checksum-verified before publishing the OCI layer
and restoring `latest`. A failure is rechecked before retrying so an API timeout
does not create a duplicate release. Verification-only runs write neither target.

The dev updater caches only a verified OCI-layer/binary digest pair beside its
binary. On each timer tick it still reads the current manifest and hashes the
installed executable. Matching both permits a no-download, no-restart check;
an invalid cache or changed executable forces full verification. A corrupt
download cannot update the cache. The timer remains enabled across logout via
systemd user lingering and reports failures in its unit journal.

An internal compaction summary is held for its whole result (`askWhole`),
even when its upstream request asks for a stream. It does not receive client
transport keepalives or turn its own HTTP errors into client-stream events.
This preserves a byte-cap refusal as 413 so compaction can retry as text and
fall back locally. Real streaming callers still receive their keepalives.

The dev updater accepts only the `application/vnd.magpie.binary.gzip` layer
from that exact GHCR repository, verifies its compressed OCI SHA-256 digest
and size, decompresses and preflights the candidate, then stops the daemon for
the atomic replacement and starts it again. Stopping first matters: an
official update already staged by the old process may install while it exits,
before the fork binary is put in place. A failed start, health check, or
post-start digest check restores `~/.local/bin/magpie.previous`.

## Bootstrap dev

Do not restart the daemon until a fork Release exists. First stop future
official downloads and confirm the saved setting:

```sh
magpie update auto off
magpie update auto
```

Download the updater and units from the newest fork Release on a trusted
computer, or copy the reviewed files from this repository. Install them:

```sh
install -d -m 700 "$HOME/.local/libexec" "$HOME/.config/systemd/user"
install -m 700 magpie-fork-update "$HOME/.local/libexec/magpie-fork-update"
install -m 600 magpie-fork-update.service "$HOME/.config/systemd/user/magpie-fork-update.service"
install -m 600 magpie-fork-update.timer "$HOME/.config/systemd/user/magpie-fork-update.timer"
systemctl --user daemon-reload
systemctl --user start magpie-fork-update.service
magpie version
magpie healthcheck
systemctl --user enable --now magpie-fork-update.timer
systemctl --user list-timers magpie-fork-update.timer
```

The first update deliberately causes one brief gateway restart. Later checks
do not restart when the installed digest already matches the newest Release.
The Release and `latest` OCI artifact contain the same tested binary. The
timer repairs `magpie update auto off` on every run.

## Recovery

If an operator needs to restore the last-known-good executable manually:

```sh
systemctl --user stop magpie.service
install -m 755 "$HOME/.local/bin/magpie.previous" "$HOME/.local/bin/magpie"
systemctl --user start magpie.service
magpie healthcheck
```

The updater lock is `~/.local/bin/magpie.fork-update.lock`. It is normally
removed by the exit trap. If the updater was killed with SIGKILL, verify no
update process is running before removing that empty directory.

To stop following the fork without changing the currently installed binary:

```sh
systemctl --user disable --now magpie-fork-update.timer
```

Re-enabling Magpie's official updater will eventually replace the fork build.
