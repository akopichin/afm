# Releases and prereleases

## Install a release channel with Homebrew

Each channel has its own cask and command. They can be installed together:

| Channel | Install | Command | Update |
|---|---|---|---|
| Primary stable (3.x after the transition) | `brew install --cask akopichin/afm/afm` | `afm` | `brew upgrade --cask afm` |
| Stable (2.x maintenance) | `brew install --cask akopichin/afm/afm@2` | `afm-2` | `brew upgrade --cask afm@2` |
| Stable (1.x) | `brew install --cask akopichin/afm/afm@1` | `afm-1` | `brew upgrade --cask afm@1` |
| Alpha | `brew install --cask akopichin/afm/afm-alpha` | `afm-alpha` | `brew upgrade --cask afm-alpha` |
| Beta | `brew install --cask akopichin/afm/afm-beta` | `afm-beta` | `brew upgrade --cask afm-beta` |

The `afm@1`/`afm@2` casks and Docker `1`/`2` tags become available after the first
maintenance release of each line published with this configuration. Until 3.0.0
is published, the primary stable installation remains the previously released
2.x version; preparing these settings does not publish or switch any channel.

The `v2.x` branch starts at `v2.2.6`. Use `make release-patch v2.x` or
`make release-minor v2.x` after pushing the reviewed maintenance commit. Major
bumps are rejected. A later 2.x release updates only its versioned artifacts,
`afm@2`, and Docker `:2`; it cannot replace the primary 3.x channels.
Prereleases of 1.x and 2.x publish versioned artifacts only. Alpha and beta casks
belong to the current product line.

An alpha or beta cask becomes available after the first release of that channel
has been published. For example:

```bash
brew install --cask akopichin/afm/afm-alpha
afm-alpha --version
afm-alpha run
```

The commands use the same project configuration and run directories as `afm`.

## AFM 3.x CI configuration

AFM owns validation, archive builds, and publication. The private `afm-engine`
repository has no active workflows. Public checks run for every push and pull
request. Product checks also run for pushes and same-repository pull requests in
`akopichin/afm`; fork and Dependabot pull requests have no private module access.
To validate an external contribution, review it and push the reviewed revision
to a branch in AFM before merging.

Configure these under AFM **Settings → Secrets and variables → Actions**:

| Kind | Name | Value |
|---|---|---|
| Repository secret | `ENGINE_READ_TOKEN` | Fine-grained token with **Contents → Read-only**, restricted to `afm-engine` |
| Repository secret | `RELEASE_TOKEN` | Token with Contents write access to `homebrew-afm` |
| Repository secrets | `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN` | Docker image publisher credentials |

Create the engine token in [GitHub developer settings](https://github.com/settings/personal-access-tokens/new).
Select resource owner `akopichin` and repository access **Only select repositories →
afm-engine**. Set an expiration and replace the secret before it expires.
Store the value through the interactive prompt:

```bash
gh secret set ENGINE_READ_TOKEN --repo akopichin/afm
```

The old `ENGINE_BUILD_DISPATCH_TOKEN` secret and `ENGINE_BUILD_REF` variable are
unused and can be removed after migration. The built-in AFM `GITHUB_TOKEN`
cannot read a different private repository; the dedicated read token is required.

Product checks download the engine version selected by AFM's `go.mod` with
`GOWORK=off`. They check out that exact Git commit for engine tests on Linux and
macOS and for the separate example and AFM integration modules. Integration
workspaces are temporary; the product build uses downloaded, pinned modules.
See [Contributing](https://github.com/akopichin/afm/blob/main/CONTRIBUTING.md)
for the engine pin update procedure. Updating a dependency requires a new AFM
commit with `go.mod` and `go.sum`; CI never follows engine `main` implicitly.

Go and linter caches are disabled in jobs handling private sources, and engine
checkouts live outside the public working tree. Only binary archives, checksums,
the build manifest, and generated Homebrew casks are retained as artifacts.
No vendor directory or private module cache is uploaded. Dashboard dependencies
use the ordinary npm cache.

Explicit version tag pushes run the same product checks alongside archive
builds. Docker preparation uses only verified archives and public runtime files.
Publication waits for validation, archives, and Docker preparation, then consumes
those artifacts without recompiling. Only the publication job has Contents write
permission and uses publisher secrets. It uses AFM's `afm-release` environment;
configure any release protection rules there and set its environment variable
`AFM_DISTRIBUTION_APPROVED=true` after resolving engine distribution. The existing
engine distribution gate remains required. No engine repository secrets are used.

## Publish a version

Start from a clean working tree and fetch tags before calculating a version:

```bash
git fetch origin --tags
```

The Make targets create an annotated Git tag and push it to `origin`. GitHub
Actions then builds the Docker image and binaries, publishes the GitHub Release,
and updates the matching Homebrew cask.

When introducing changes to the release workflow, commit and push them to the
branch being released and wait for CI to succeed. Each tag uses the workflow
and GoReleaser configuration from its own commit. Then fetch tags and run the
desired release target. The target itself pushes the tag; no additional `git push` is needed. Branch pushes run CI checks and do
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
the `2.0.0` prerelease is ready for a stable release.

The `v1.x` maintenance branch starts at `v1.2.17` and has its own stable release
line. Push changes to that branch, then run from any checkout with the
updated release tooling:

```bash
git push origin v1.x
make release-patch v1.x   # v1.2.17 -> v1.2.18
# Or choose a minor release instead:
make release-minor v1.x   # v1.2.17 -> v1.3.0
```

An explicit branch argument fetches that branch and tags from `origin`, captures
its published commit SHA, and tags that SHA. It does not switch branches or use
uncommitted changes, local-only commits, or the caller's local branch tip. A dirty
caller checkout is allowed in this mode. Without a branch argument, the existing
clean-checkout requirement and `HEAD` behavior apply. `main` is also supported:
`make release-patch main`.

The version base is the highest stable tag reachable from the selected commit.
Maintenance branches named `vMAJOR.x` must have a matching stable version base;
a major bump is rejected on these branches. Tags remain global, so an existing
tag outside the branch also prevents reusing that version. Branch publication
does not create a release until a release command pushes a version tag.

Preview the remote maintenance release without creating or pushing a tag:

```bash
./scripts/release.sh --dry-run patch --branch v1.x
./scripts/release.sh --dry-run minor --branch v1.x
```

Preview the next version without creating or pushing a tag:

```bash
./scripts/release.sh --dry-run major alpha
./scripts/release.sh --dry-run minor beta
./scripts/release.sh --dry-run patch
```

## Published artifacts

Alpha and beta versions are marked as prereleases in GitHub. They update only
their own Homebrew cask, and their Docker image is available by its exact tag,
for example `akopichin/afm:v2.0.0-alpha.1`. Stable `2.x` releases update the
`afm` cask and Docker `latest`. Stable `1.x` releases update only `afm@1` and Docker `1`, as well as their exact version tag.
They are stable GitHub Releases, but never replace GitHub's Latest release.
`1.x` prereleases publish exact-tag artifacts without updating any Homebrew cask
or floating Docker tag.

Release archives contain a binary named `afm` (`afm.exe` on Windows). Homebrew
renames the installed prerelease binary to `afm-alpha` or `afm-beta` so the
commands can coexist with the stable version.

The `afm@1` cask installs the archive's `afm` binary as `afm-1`; upgrades keep
that command on the latest stable `1.x`, including minor releases. In the `v1.x`
branch, Docker mode defaults to `akopichin/afm:1` so it stays on the same major
line. An explicit `docker.image` or `AFM_DOCKER_IMAGE` still takes precedence.
Both commands use the same project configuration and run directories; use
separate projects or `--dir` values when keeping independent runs for each major.
