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

Public CI validates contracts, SDK, and dashboard changes. After these checks
succeed on a push to `akopichin/afm`'s `main`, it requests `afm-release.yml` in the
private `akopichin/afm-engine` repository with the exact AFM commit and
`publish=false`. Pull requests and other branches do not dispatch private builds.
The public job confirms that GitHub accepted the request; the full build result
and archives are available in the private repository's Actions run.

Configure these under AFM **Settings → Secrets and variables → Actions**:

| Kind | Name | Value |
|---|---|---|
| Repository variable | `ENGINE_BUILD_REF` | A reviewed engine workflow branch or tag, currently `main` |
| Repository secret | `ENGINE_BUILD_DISPATCH_TOKEN` | A dedicated fine-grained personal access token |

Create the token in [GitHub developer settings](https://github.com/settings/personal-access-tokens/new).
Select resource owner `akopichin`, repository access **Only select repositories →
afm-engine**, and repository permission **Actions → Read and write**. Set an
expiration and replace the secret before it expires. The token can operate on
Actions in that repository; it is not restricted to a single workflow. It does
not need Contents write permission. Store it directly in GitHub or use the
interactive prompt; do not put the value in command arguments:

```bash
gh secret set ENGINE_BUILD_DISPATCH_TOKEN --repo akopichin/afm
gh variable set ENGINE_BUILD_REF --body main --repo akopichin/afm
```

The engine revision used by the product still comes from AFM's `go.mod`.
`ENGINE_BUILD_REF` selects the workflow and integration-test checkout. Engine
module downloads use the private repository's built-in `GITHUB_TOKEN`.
Engine CI runs the internal engine suite on Linux and macOS. The AFM product
workflow runs AFM, UI, SDK, and shared-host integration tests without repeating
the internal engine suite. Pin only an engine revision with successful Engine CI.
The workflow uses private Go/npm caches and separate validation steps, with lint
before the long test suites. SDK unit and integration tests run together once;
their results are not cached because they exercise the separately built binary.
Updating the workflow ref does not change the engine module version in `go.mod`.

For actual publication, the private engine's `afm-release` environment also needs
`AFM_PUBLISH_TOKEN` (AFM Contents write), `HOMEBREW_PUBLISH_TOKEN`
(`homebrew-afm` Contents write), and `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` for the
image publisher. These are not needed for validation-only builds. Enable
`AFM_DISTRIBUTION_APPROVED=true` there only after resolving engine distribution.
Existing AFM secrets cannot be read back through GitHub's API to copy them.

The temporary `engine-contracts` branch can be deleted after the clean `main`
has been pushed and contains its commits. Pinned contracts versions remain
reachable through `main` and need no version changes for that deletion.

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
