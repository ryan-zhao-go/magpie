# Fork Auto-Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Continuously rebuild the answer-timeout fork on current upstream `main`, publish a verified Linux ARM64 release, and update the dev daemon automatically with rollback.

**Architecture:** A scheduled GitHub Actions workflow checks out the fork patch branch, rebases it ephemerally onto `yetone/magpie/main`, runs the answer-timeout and configuration tests, cross-builds the CLI, and publishes an immutable fork release with a SHA-256 file. A user-level systemd timer downloads only that named asset from the public fork, verifies its digest, atomically replaces the daemon binary, and restores the previous binary if the restarted gateway does not pass `magpie healthcheck`.

**Tech Stack:** GitHub Actions, Bash/POSIX shell, Go, GitHub Releases, systemd user units.

## Global Constraints

- The official Magpie updater must be disabled on dev before any service restart.
- The workflow must never push a rebase or force-push the patch branch.
- A rebase conflict, failed test, or failed build publishes nothing.
- Releases must contain `magpie-cli-linux-arm64` and `magpie-cli-linux-arm64.sha256`.
- The updater must accept only HTTPS GitHub API/download URLs for `ryan-zhao-go/magpie` and the exact asset names above.
- Installation must use temp-file plus atomic rename, retain one last-known-good binary, and roll it back after failed restart or health check.
- The existing provider configuration and routing member order must not be changed.

---

### Task 1: Release metadata and updater contract

**Files:**
- Create: `fork/release-meta.sh`
- Create: `fork/release-meta-test.sh`

**Interfaces:**
- Consumes: an upstream version tag, upstream commit SHA, patch commit SHA, and UTC timestamp.
- Produces: validated `VERSION`, `RELEASE_TAG`, and `RELEASE_NAME` values in GitHub output format.

- [ ] **Step 1: Write tests for accepted metadata and rejected unsafe inputs**

  The test invokes the script with a temporary `GITHUB_OUTPUT`, asserts a semver prerelease such as `0.1.2732-fork.20261102153040`, and verifies that slashes, whitespace, and malformed versions fail.

- [ ] **Step 2: Run the test and verify it fails because the implementation is absent**

  Run: `bash fork/release-meta-test.sh`
  Expected: non-zero with `fork/release-meta.sh` missing.

- [ ] **Step 3: Implement strict metadata generation**

  Accept exactly four arguments, strip one leading `v` from the version, validate all inputs with shell `case` patterns and explicit character checks, and append three key/value lines to `$GITHUB_OUTPUT`.

- [ ] **Step 4: Run the metadata tests**

  Run: `bash fork/release-meta-test.sh`
  Expected: `release-meta tests passed`.

### Task 2: Fail-closed dev updater

**Files:**
- Create: `fork/magpie-fork-update`
- Create: `fork/magpie-fork-update-test.sh`

**Interfaces:**
- Consumes: GitHub's latest-release JSON, two exact release assets, `MAGPIE_BIN`, and the user service named by `MAGPIE_SERVICE`.
- Produces: an atomically installed executable, `<binary>.previous`, and a restarted healthy daemon; returns non-zero with the old executable restored on any failure.

- [ ] **Step 1: Build a hermetic command-fixture test**

  Put mock `curl` and `systemctl` programs first on `PATH`. Cover: valid install, unchanged digest, malformed JSON, wrong repository download URL, bad checksum, and healthcheck failure restoring the old bytes.

- [ ] **Step 2: Run the updater test and verify it fails without the updater**

  Run: `bash fork/magpie-fork-update-test.sh`
  Expected: non-zero because `fork/magpie-fork-update` is absent.

- [ ] **Step 3: Implement download validation, atomic install, and rollback**

  Parse JSON with `python3`, constrain both URLs to the fork release-download prefix, download into `mktemp -d`, verify `sha256sum`, reject non-regular/empty files, compare the installed digest, move the existing binary to `.previous`, rename the verified binary into place, restart, and poll `<binary> healthcheck`. On failure restore `.previous`, restart it, and return non-zero. Use an `EXIT` trap to remove only the validated temporary directory.

- [ ] **Step 4: Run updater tests and syntax checks**

  Run: `bash fork/magpie-fork-update-test.sh && sh -n fork/magpie-fork-update`
  Expected: all updater cases pass and syntax check exits zero.

### Task 3: Scheduled fork release workflow

**Files:**
- Create: `.github/workflows/fork-release.yml`

**Interfaces:**
- Consumes: public upstream/fork repositories and the patch branch on which the workflow lives.
- Produces: an immutable normal GitHub Release containing the binary, checksum, updater, and systemd unit templates.

- [ ] **Step 1: Add workflow triggers and least privileges**

  Run hourly and by manual dispatch, use `contents: write`, serialize with `fork-release`, and pin the source checkout to `feature/answer-timeout`.

- [ ] **Step 2: Add ephemeral upstream replay**

  Fetch upstream `main` and tags, record upstream and patch SHAs, then run `git rebase upstream/main`. Never push the result. Generate a deterministic release tag from source SHAs and exit successfully when that release already exists.

- [ ] **Step 3: Add release gates**

  Install Go and Bun, run provider answer-timeout tests, the focused gateway answer-timeout suite, the routing UI test in Chromium and WebKit, then build `GOOS=linux GOARCH=arm64` with the generated fork version.

- [ ] **Step 4: Publish immutable assets**

  Generate the SHA-256 sidecar and publish the binary, checksum, updater, service, and timer with notes containing both source SHAs and the verification commands.

- [ ] **Step 5: Validate workflow structure locally**

  Parse the YAML, check all referenced paths exist, run `git diff --check`, and inspect permissions/triggers/assets.

### Task 4: systemd deployment templates and operations guide

**Files:**
- Create: `fork/systemd/magpie-fork-update.service`
- Create: `fork/systemd/magpie-fork-update.timer`
- Create: `fork/README.md`

**Interfaces:**
- Consumes: updater installed at `%h/.local/libexec/magpie-fork-update` and Magpie at `%h/.local/bin/magpie`.
- Produces: an hourly persistent user timer and an explicit install/rollback/runbook.

- [ ] **Step 1: Add hardened user unit and timer**

  The service is oneshot, has a 10-minute timeout, uses `NoNewPrivileges`, `PrivateTmp`, and filesystem protections compatible with writing `%h/.local/bin`. The timer runs after boot and hourly with randomized delay and persistence.

- [ ] **Step 2: Document bootstrap and recovery**

  Include exact commands to disable the official updater, install the updater and units, perform the first controlled update, verify version/health/timer, restore `.previous`, and remove the fork updater.

- [ ] **Step 3: Verify all shell tests and repository checks**

  Run: `bash fork/release-meta-test.sh && bash fork/magpie-fork-update-test.sh && sh -n fork/release-meta.sh fork/magpie-fork-update && git diff --check`
  Expected: zero exit status and both test success messages.

### Task 5: Publish and bootstrap dev

**Files:**
- Modify only through Git/GitHub and dev's `%h/.local` plus `%h/.config/systemd/user` paths.

**Interfaces:**
- Consumes: committed/pushed workflow and restored SSH authentication.
- Produces: a successful fork Release and a dev host following the fork update timer.

- [ ] **Step 1: Commit and push the automation**

  Push to `fork/feature/answer-timeout`, make it the fork default branch so scheduled workflows are read from it, enable Actions write permission, and dispatch the workflow.

- [ ] **Step 2: Verify the GitHub run and release**

  Wait for the workflow, inspect every job, download the assets, and independently verify SHA-256 and embedded source/version metadata.

- [ ] **Step 3: Bootstrap dev without an uncontrolled restart**

  Disable official updates and read the setting back. Install the updater and units, run the updater once, then enable the timer. Do not restart until the fork release exists and the updater has the rollback binary ready.

- [ ] **Step 4: Verify dev end-to-end**

  Verify `magpie version`, `magpie healthcheck`, `systemctl --user is-active magpie`, `systemctl --user is-enabled --now magpie-fork-update.timer`, the deployed SHA against the Release sidecar, and the three stable groups' `answerTimeout: 60` without changing member order.
