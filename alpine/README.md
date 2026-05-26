# Alpine Linux packaging

dotkeeper ships as a first-class Alpine package: a native `.apk` built
with `abuild` against the upstream toolchain, suitable both for
side-loading (`apk add` on a `.apk` file) and for upstream submission
into Alpine's aports tree.

The native build is **distinct** from the `.apk` produced by the
release pipeline's `nfpm` step. `nfpm` produces a generic `.apk`
that works for most tools but doesn't carry the metadata that
`aports` expects — for upstream submission we need a real
`abuild`-produced package. Both exist in releases today; the native
one is the authoritative source for Alpine users.

## Files in this directory

| File         | Purpose |
|--------------|---------|
| `APKBUILD`   | Alpine package source. Source-based build (pulls the GitHub release tarball, runs `go build`, packages the binary). Aports-conformant: same file is shippable upstream. |
| `README.md`  | This file. |

## How the native build runs

Every push of a `v*` tag triggers two release workflows:

1. **`.github/workflows/release.yml`** — cross-compiles binaries for
   13 OS/arch combos, builds `.deb`/`.rpm`/`.apk`/`.pkg.tar.zst` via
   `nfpm`, uploads everything to the GitHub release page.
2. **`.github/workflows/alpine.yml`** — runs after the release is
   published, builds the APKBUILD inside an `alpine:edge` container
   via `abuild`, uploads the resulting native `.apk` to the same
   release.

The native `.apk` has filename `dotkeeper_<version>_alpine_<arch>.apk`
to distinguish from the nfpm one. The CI uploads the main package
only; the `-doc` subpackage (created for upstream conformance) is
built but not attached to the GitHub release.

## Installing on Alpine

For an end user who wants the package today (before it lands in
Alpine's official repos), download the native `.apk` from the release
page and install it locally:

```sh
apk add --allow-untrusted dotkeeper_1.2.7_alpine_x86_64.apk
```

The `--allow-untrusted` flag is needed because the CI build uses a
disposable signing key per run (not added to the system's apk
keyring). After upstream submission lands in aports, this flag will
no longer be needed.

## Maintaining the APKBUILD

The repo file at `alpine/APKBUILD` carries a placeholder `pkgver=0.0.0`.
The CI workflow patches this in-place to the release tag's version
before running `abuild`. **Do not commit a real version to the
in-repo APKBUILD** — that would mean every release would also need a
PR to bump the file, defeating the workflow's purpose.

When changing build deps, install targets, or the test command, edit
`alpine/APKBUILD` directly. The next release will pick up the changes.

## Upstream submission to aports

Alpine aports lives at <https://gitlab.alpinelinux.org/alpine/aports>.
New packages enter via the `testing/` directory and graduate to
`community/` over time as maintainership stabilises. This in-repo
APKBUILD is already shaped for aports (arch="all", `-doc` subpackage,
`net` build option, no CI-specific `!tracedeps`), so the submission
flow is mostly copy-paste + patch-version-in.

### One-time account setup

1. Create an account at <https://gitlab.alpinelinux.org/>. This is a
   separate identity from gitlab.com; Alpine's GitLab is self-hosted.
2. Add an SSH key under your profile so you can `git push` to your
   fork.
3. Fork <https://gitlab.alpinelinux.org/alpine/aports> into your
   personal namespace. Use HTTPS or SSH for the clone URL depending
   on your auth setup.

### One-time local build environment

Either run a persistent Alpine VM/container with abuild set up, or
spin up a fresh container per submission. The container-per-submission
flow is reproducible and what dotkeeper's own CI uses; the script
below mirrors that.

```sh
# Inside an `alpine:edge` container with the cloned aports tree mounted:
apk add --no-cache alpine-sdk
adduser -D -G abuild builder
mkdir -p /home/builder/.abuild
abuild-keygen -a -i -n           # interactive on first run
```

### Per-version submission

```sh
cd <your-aports-fork>
git fetch origin && git checkout -b dotkeeper-1.2.7 origin/master
mkdir -p testing/dotkeeper
cp <dotkeeper-repo>/alpine/APKBUILD testing/dotkeeper/APKBUILD
sed -i 's/^pkgver=0\.0\.0/pkgver=1.2.7/' testing/dotkeeper/APKBUILD
cd testing/dotkeeper
abuild checksum                  # fills sha512sums
abuild -F -r                     # local test build (validates the package)
git add APKBUILD
git commit -m 'testing/dotkeeper: new aport'
git push origin dotkeeper-1.2.7
```

Then open a Merge Request on gitlab.alpinelinux.org targeting
`master`. Alpine maintainers review; once merged, the package becomes
available on edge as `apk add dotkeeper@testing`, and on subsequent
stable releases.

### Subsequent version bumps

After the first submission lands, bumps follow the same flow with a
slightly different commit message:

```sh
git checkout -b dotkeeper-1.2.8 origin/master
sed -i 's/^pkgver=.*/pkgver=1.2.8/' testing/dotkeeper/APKBUILD
abuild checksum
abuild -F -r
git add testing/dotkeeper/APKBUILD
git commit -m 'testing/dotkeeper: upgrade to 1.2.8'
git push origin dotkeeper-1.2.8
```

(If the package has graduated from `testing/` to `community/` by then,
substitute `community/dotkeeper` in the paths and `community/dotkeeper:`
in the commit prefix.)

## Why not automate the upstream submission?

Alpine's submission process expects a human in the loop: each MR
should pass `abuild checksum` + `abuild -F -r` locally, the submitter
should be reachable for review comments, and the commit message
should match Alpine's conventions. Automating it would either skip
those steps (lower quality) or layer enough scaffolding around them
that the automation itself becomes the maintenance burden. At
dotkeeper's current release cadence, manual is the right tradeoff.
