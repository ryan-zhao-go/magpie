# Magpie fork release and dev updates

This directory keeps the answer-timeout patch on current upstream Magpie
without letting Magpie's official updater replace it. The scheduled workflow
rebases the patch branch in a disposable runner, tests that tree, and publishes
an immutable Linux AMD64 release. It also publishes the tested binary as a
public OCI artifact at `ghcr.io/ryan-zhao-go/magpie-fork:latest` for hosts that
cannot reach GitHub's Release web endpoint. It never force-pushes the patch
branch.

The dev updater accepts only the `application/vnd.magpie.binary` layer from
that exact GHCR repository, verifies its OCI SHA-256 digest and size, stops the
daemon before the atomic replacement, and starts it again. Stopping first
matters: an official update already staged by the old process may install
while it exits, before the fork binary is put in place. A failed start, health
check, or post-start digest check restores `~/.local/bin/magpie.previous`.

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
