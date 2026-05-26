# Alpine Linux packaging

dotkeeper ships as a first-class Alpine package: a native `.apk` built
with `abuild` against the upstream toolchain, suitable both for
side-loading (`apk add` on a `.apk` file) and for upstream submission
into `aports/community`.

The native build is **distinct** from the `.apk` produced by the
release pipeline's `nfpm` step. `nfpm` produces a generic `.apk`
that works for most tools but doesn't carry the metadata that
`aports` expects — for upstream submission we need a real
`abuild`-produced package. Both exist in releases today; the native
one is the authoritative source for Alpine users.

## Files in this directory

| File         | Purpose |
|--------------|---------|
| `APKBUILD`   | Alpine package source. Source-based build (pulls the GitHub release tarball, runs `make build`, packages the binary). |
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
to distinguish from the nfpm one.

## Installing on Alpine

For an end user who wants the package today (before it lands in
`aports/community`), download the native `.apk` from the release page
and install it locally:

```sh
apk add --allow-untrusted dotkeeper_1.2.2_alpine_x86_64.apk
```

The `--allow-untrusted` flag is needed because the CI build uses a
disposable signing key per run (not added to the system's apk
keyring). After upstream submission into `aports/community`, this
flag will no longer be needed.

## Maintaining the APKBUILD

The repo file at `alpine/APKBUILD` carries a placeholder `pkgver=0.0.0`.
The CI workflow patches this in-place to the release tag's version
before running `abuild`. **Do not commit a real version to the
in-repo APKBUILD** — that would mean every release would also need a
PR to bump the file, defeating the workflow's purpose.

When changing build deps, install targets, or the test command, edit
`alpine/APKBUILD` directly. The next release will pick up the changes.

## Upstream submission to `aports/community`

These steps are **manual** and only run when we want a new dotkeeper
release to land in Alpine's official package repository. Subsequent
version bumps follow the same flow.

1. **Account setup** (one-time):
   - Create an account at <https://gitlab.alpinelinux.org/>.
   - Fork <https://gitlab.alpinelinux.org/alpine/aports> into your
     personal namespace.

2. **Local build environment** (per machine, one-time):
   - Run an Alpine edge container: `docker run --rm -it alpine:edge sh`
   - Install build tools: `apk add alpine-sdk`
   - Add yourself to the abuild group: `adduser -G abuild $USER`
   - Generate a signing key: `abuild-keygen -a -i`

3. **Create the submission**:
   ```sh
   git clone git@gitlab.alpinelinux.org:<your-namespace>/aports.git
   cd aports
   git checkout -b dotkeeper-1.2.2
   mkdir -p community/dotkeeper
   cp <dotkeeper-repo>/alpine/APKBUILD community/dotkeeper/APKBUILD
   sed -i 's/^pkgver=0\.0\.0/pkgver=1.2.2/' community/dotkeeper/APKBUILD
   cd community/dotkeeper
   abuild checksum    # fills sha512sums
   abuild -r          # test-build the package
   ```

4. **Open MR**:
   ```sh
   git add APKBUILD
   git commit -m 'community/dotkeeper: new aport'  # or 'upgrade to 1.2.2' for bumps
   git push origin dotkeeper-1.2.2
   ```
   Then open a Merge Request on gitlab.alpinelinux.org targeting
   `master`. Alpine maintainers review; once merged, the package
   becomes available via `apk add dotkeeper` on Alpine edge, and on
   subsequent stable releases.

5. **Subsequent version bumps**: same flow, except step 3 starts from
   a fresh branch and the commit message is
   `community/dotkeeper: upgrade to <version>`.

## Why not automate the upstream submission?

Alpine's submission process expects a human in the loop: each MR
should pass `abuild checksum` + `abuild -r` locally, the submitter
should be reachable for review comments, and the commit message
should match Alpine's conventions. Automating it would either skip
those steps (lower quality) or layer enough scaffolding around them
that the automation itself becomes the maintenance burden. Once a
quarter at the current release cadence, manual is the right tradeoff.
