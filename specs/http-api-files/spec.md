# HTTP API: file browser and run-file reads

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Read-only project file browser (`/api/files/*`) and its containment boundary.
- Reads of run files referenced by client-supplied ids (verify reports).
- Out of scope: modes, guards and stage views (`../http-api/spec.md`); live updates and SDK
  (`../http-api-live/spec.md`). Part of the `API` family; IDs continue the shared numbering.

## Terms
- **manifest roots**: allowed browse roots handed over by the host (`contracts/docker` `FileRootManifest`).

## Requirements

### API-R13 File browser stays inside manifest roots and is read-only
Roots come from the host manifest: the project and only `browse: true` extra mounts. Raw rel-paths are checked
before cleaning (no traversal, absolute, NUL, `.git`/`.afm`); access uses a root fd with Linux `openat2`
containment and no symlinks; without that capability the browser is off. Reads open regular files only
(FIFOs never hang). The API is GET-only, 404 when disabled, and hides absolute roots and raw git stderr.

Evidence: `afm:contracts/docker/manifest_test.go` `TestBuildFileRootManifest_OnlyBrowseable`;
`afm:pkg/server/workspace/roots_test.go` `TestValidateRelPath`; `afm:pkg/server/workspace/access_linux_test.go`
`TestOpenat_BlocksTraversalAndSymlink`; `afm:pkg/server/workspace/fifo_linux_test.go` `TestReadReference_FIFO_DoesNotHang`;
`afm:pkg/server/workspace/diff_test.go` `TestDiff_GitSymlinkRefused`, `TestDiff_GitfileOutsideRootRefused`;
`afm:pkg/server/files_handlers_test.go` `TestFiles_DisabledReturns404`, `TestFilesChanged_NonGET`

#### API-S10 symlink escape
- GIVEN a browseable root containing a symlink to `/etc`
- WHEN the client reads a path through that symlink
- THEN the read is refused

#### API-S11 private extra mount
- GIVEN an `extra_mounts` entry without `browse: true`
- WHEN the host builds the file-root manifest
- THEN that mount is not a browse root and its path is not exposed

### API-R14 Verify reports are read through the run root
`GET` of a stage verify report resolves via `os.OpenRoot(runDir)`; invalid stage/verification ids are rejected
and symlink escapes refused, so a client id never yields an arbitrary file read.

Evidence: `afm:pkg/server/verify_report_test.go` `TestHandleVerifyReport_RejectsSymlinkEscape`,
`TestHandleVerifyReport_RejectsInvalidVerificationID`, `TestHandleVerifyReport_RejectsNonGET`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| API-E12 | workspace disabled / capability unsupported | 404 on `/api/files/*` | `TestFiles_DisabledReturns404` |
| API-E13 | non-GET on files API | 405 | `TestFilesChanged_NonGET` |
| API-E14 | invalid verification or stage id | 400 | `TestHandleVerifyReport_RejectsInvalidVerificationID` |

## Coverage gaps and discrepancies
- **API-G05** (untested on macOS): workspace containment tests are `//go:build linux`; macOS runs disable the
  browser rather than test it.
