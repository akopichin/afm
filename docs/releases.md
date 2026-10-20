# Releases and prereleases

## Install a release channel with Homebrew

Each channel has its own cask and command. They can be installed together:

| Channel | Install | Command | Update |
|---|---|---|---|
| Stable | `brew install --cask akopichin/afm/afm` | `afm` | `brew upgrade --cask afm` |
| Alpha | `brew install --cask akopichin/afm/afm-alpha` | `afm-alpha` | `brew upgrade --cask afm-alpha` |
| Beta | `brew install --cask akopichin/afm/afm-beta` | `afm-beta` | `brew upgrade --cask afm-beta` |

An alpha or beta cask becomes available after the first release of that channel
has been published. For example:

```bash
brew install --cask akopichin/afm/afm-alpha
afm-alpha --version
afm-alpha run
```

The commands use the same project configuration and run directories as `afm`.

## Publish a version

Start from a clean working tree and fetch tags before calculating a version:

```bash
git fetch origin --tags
```

The Make targets create an annotated Git tag and push it to `origin`. GitHub
Actions then builds the Docker image and binaries, publishes the GitHub Release,
and updates the matching Homebrew cask.

When introducing changes to the release workflow, commit and push them to `main`
first and wait for CI to succeed. Then fetch tags and run the desired release
target from a clean checkout containing those changes. The target itself pushes
the tag; no additional `git push` is needed. Branch pushes run CI checks and do
not create release tags. This lets `main` contain alpha or beta code without
publishing it as a stable `1.x` release.

| Version bump | Stable | Alpha | Beta |
|---|---|---|---|
| Major | `make release-major` | `make release-major-alpha` | `make release-major-beta` |
| Minor | `make release-minor` | `make release-minor-alpha` | `make release-minor-beta` |
| Patch | `make release-patch` | `make release-patch-alpha` | `make release-patch-beta` |

The base is the highest stable `vMAJOR.MINOR.PATCH` tag reachable from `HEAD`,
or `v0.0.0` if there is none. Prerelease tags do not change that base. The counter
starts at 1 and increases independently for each target version and channel,
including matching local tags on other branches to avoid reusing a tag name.

For example, starting from stable `v1.2.17`, with no existing `v2.0.0` tags:

```bash
make release-major-alpha  # v2.0.0-alpha.1
make release-major-alpha  # v2.0.0-alpha.2
make release-major-beta   # v2.0.0-beta.1
make release-major-beta   # v2.0.0-beta.2
make release-major        # v2.0.0
```

After `v2.0.0`, the next `make release-major-alpha` starts `v3.0.0-alpha.1`.
Stable releases also require an explicit command. Use `make release-major` when
the `2.0.0` prerelease is ready for a stable release. For a stable `1.x` fix,
run `make release-patch` from a maintenance branch based on a stable `1.x` tag,
with only that fix applied.

Preview the next version without creating or pushing a tag:

```bash
./scripts/release.sh --dry-run major alpha
./scripts/release.sh --dry-run minor beta
./scripts/release.sh --dry-run patch
```

## Published artifacts

Alpha and beta versions are marked as prereleases in GitHub. They update only
their own Homebrew cask, and their Docker image is available by its exact tag,
for example `akopichin/afm:v2.0.0-alpha.1`. Stable releases update the `afm` cask
and Docker `latest`.

Release archives contain a binary named `afm` (`afm.exe` on Windows). Homebrew
renames the installed prerelease binary to `afm-alpha` or `afm-beta` so the
commands can coexist with the stable version.
