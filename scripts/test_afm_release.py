import hashlib
import io
import json
import pathlib
import tarfile
import tempfile
import unittest
from unittest import mock

import yaml

import afm_docker_context as docker_context
import checkout_engine


class ReleaseWorkflowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = pathlib.Path(__file__).parents[1] / ".github/workflows/release.yml"
        cls.workflow = yaml.safe_load(path.read_text())
        cls.jobs = cls.workflow["jobs"]

    def test_builds_overlap_validation_and_publication_waits_for_both(self):
        self.assertEqual(self.jobs["validate"]["uses"], "./.github/workflows/product-checks.yml")
        self.assertNotIn("needs", self.jobs["artifacts"])
        self.assertEqual(self.jobs["docker"]["needs"], "artifacts")
        publish = self.jobs["publish"]
        self.assertEqual(set(publish["needs"]), {"validate", "artifacts", "docker"})
        self.assertNotIn("if", publish)
        self.assertEqual(publish["environment"], "afm-release")
        self.assertEqual(self.workflow[True], {"push": {"tags": ["v*.*.*"]}})

    def test_preparation_cannot_publish_or_access_publisher_secrets(self):
        for name in ("validate", "artifacts", "docker"):
            self.assertNotIn("environment", self.jobs[name])
            self.assertNotIn("RELEASE_TOKEN", json.dumps(self.jobs[name]))
            self.assertNotIn("DOCKERHUB_TOKEN", json.dumps(self.jobs[name]))
            for step in self.jobs[name].get("steps", []):
                self.assertNotEqual(step.get("with", {}).get("push"), True)
                self.assertNotIn("publish-archives.sh", step.get("run", ""))
        build = next(s for s in self.jobs["docker"]["steps"] if s.get("uses") == "docker/build-push-action@v6")
        self.assertEqual(build["with"]["outputs"], "type=cacheonly")
        self.assertEqual(build["with"]["cache-to"], "type=gha,scope=afm-runtime,mode=max")
        self.assertEqual(self.workflow["permissions"], {"contents": "read"})

    def test_all_checks_and_uncached_sdk_integration_are_retained(self):
        path = pathlib.Path(__file__).parents[1] / ".github/workflows/product-checks.yml"
        checks = yaml.safe_load(path.read_text())["jobs"]["validate"]
        commands = [s.get("run", "") for s in checks["steps"]]
        for command in ("make build", "make lint-ci sdk-lint", 'make test TESTFLAGS="-p 1"',
                        "make web-test", "make web-typecheck", "make schema-check"):
            self.assertIn(command, commands)
        sdk = [s for s in checks["steps"] if s.get("working-directory") == "sdk"]
        self.assertEqual(len(sdk), 1)
        self.assertIn("-race -count=1", sdk[0]["run"])
        self.assertEqual(sdk[0]["env"]["AFM_SDK_TEST_BINARY"], "${{ github.workspace }}/bin/afm")
        self.assertTrue(any("-tags afmintegration -race -count=1" in c for c in commands))
        self.assertTrue(any("mkdocs build --strict" in c for c in commands))

    def test_publisher_consumes_artifacts_without_recompiling(self):
        upload = next(s for s in self.jobs["artifacts"]["steps"] if s.get("uses") == "actions/upload-artifact@v4")
        for job in ("docker", "publish"):
            download = next(s for s in self.jobs[job]["steps"] if s.get("uses") == "actions/download-artifact@v4")
            self.assertEqual(download["with"]["name"], upload["with"]["name"])
            self.assertNotIn("go build", json.dumps(self.jobs[job]))
            self.assertNotIn("goreleaser", json.dumps(self.jobs[job]))
        self.assertEqual(set(upload["with"]["path"].split()), {
            "dist/*.tar.gz", "dist/*.zip", "dist/checksums.txt",
            "dist/build-manifest.json", "dist/homebrew/Casks/*.rb",
        })
        publish = next(s for s in self.jobs["publish"]["steps"] if s.get("uses") == "docker/build-push-action@v6")
        self.assertTrue(publish["with"]["push"])
        self.assertEqual(publish["with"]["cache-from"], "type=gha,scope=afm-runtime")

    def test_existing_distribution_gate_and_least_privilege_are_preserved(self):
        publish = self.jobs["publish"]
        self.assertEqual(publish["permissions"], {"contents": "write"})
        gate = next(s for s in publish["steps"] if s.get("name") == "Verify distribution approval")
        self.assertEqual(gate["env"]["DISTRIBUTION_APPROVED"], "${{ vars.AFM_DISTRIBUTION_APPROVED }}")
        self.assertEqual(gate["run"], 'test "$DISTRIBUTION_APPROVED" = true')
        self.assertNotIn("ENGINE_READ_TOKEN", json.dumps(publish))
        for name in ("validate", "artifacts", "docker"):
            self.assertNotIn("permissions", self.jobs[name])


class ProductWorkflowTests(unittest.TestCase):
    def setUp(self):
        root = pathlib.Path(__file__).parents[1]
        self.ci = yaml.safe_load((root / ".github/workflows/ci.yml").read_text())
        self.checks = yaml.safe_load((root / ".github/workflows/product-checks.yml").read_text())
        self.action = yaml.safe_load((root / ".github/actions/private-go/action.yml").read_text())

    def test_untrusted_prs_never_receive_private_module_credentials(self):
        self.assertEqual(self.ci[True], {"push": None, "pull_request": None})
        guard = self.ci["jobs"]["product"]["if"]
        self.assertIn("github.repository == 'akopichin/afm'", guard)
        self.assertIn("github.event_name == 'push'", guard)
        self.assertIn("github.event.pull_request.head.repo.full_name == github.repository", guard)
        self.assertIn("github.actor != 'dependabot[bot]'", guard)
        self.assertNotIn("secrets.", json.dumps(self.ci["jobs"]["validate"]))

    def test_private_sources_are_not_cached_or_uploaded(self):
        for job in self.checks["jobs"].values():
            steps = job["steps"]
            self.assertFalse(any(s.get("uses", "").startswith("actions/upload-artifact") for s in steps))
            cleanup = next(s for s in steps if s.get("name") == "Remove module credentials")
            self.assertEqual(cleanup["if"], "always()")
            self.assertIn('rm -f "$HOME/.netrc"', cleanup["run"])
            for step in steps:
                if step.get("uses", "").startswith("golangci/"):
                    self.assertTrue(step["with"]["skip-cache"])
        setup = next(s for s in self.action["runs"]["steps"] if s.get("uses", "").startswith("actions/setup-go"))
        self.assertFalse(setup["with"]["cache"])
        self.assertEqual(self.checks["env"]["GOWORK"], "off")

    def test_engine_checks_cover_both_platforms_and_use_the_module_pin(self):
        engine = self.checks["jobs"]["engine"]
        self.assertEqual(set(engine["strategy"]["matrix"]["os"]), {"ubuntu-latest", "macos-latest"})
        for job in self.checks["jobs"].values():
            commands = "\n".join(s.get("run", "") for s in job["steps"])
            self.assertIn('python3 scripts/checkout_engine.py "$RUNNER_TEMP/afm-engine"', commands)
            self.assertNotIn("ENGINE_BUILD_REF", commands)
        commands = "\n".join(s.get("run", "") for s in engine["steps"])
        self.assertIn('make test TESTFLAGS="-p 1"', commands)
        self.assertIn("make example-test example-build", commands)


class EnginePinTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.info = pathlib.Path(self.temp.name) / "module.info"
        self.revision = "a" * 40
        self.info.write_text(json.dumps({"Origin": {"Hash": self.revision}}))

    def test_revision_comes_from_the_selected_module_not_a_branch(self):
        for origin in ({}, {"Origin": {"Hash": self.revision}}):
            with mock.patch.object(checkout_engine, "output", side_effect=[
                json.dumps({"Version": "v1.0.3"}), json.dumps({"Info": str(self.info), **origin})
            ]) as command:
                self.assertEqual(checkout_engine.pinned_revision(), self.revision)
                self.assertEqual(command.call_args.args[-1], checkout_engine.MODULE + "@v1.0.3")

    def test_workspaces_replacements_and_missing_revision_are_rejected(self):
        for module in ({"Main": True}, {"Replace": {"Dir": "/local/engine"}}):
            with mock.patch.object(checkout_engine, "output", return_value=json.dumps(module)):
                with self.assertRaisesRegex(ValueError, "pinned module"):
                    checkout_engine.pinned_revision()
        self.info.write_text("{}")
        with mock.patch.object(checkout_engine, "output", side_effect=[
            json.dumps({"Version": "v1.0.3"}), json.dumps({"Info": str(self.info)})
        ]):
            with self.assertRaisesRegex(ValueError, "exact engine Git revision"):
                checkout_engine.pinned_revision()

    def test_checkout_fetches_and_verifies_the_exact_commit(self):
        destination = pathlib.Path(self.temp.name) / "engine"
        with mock.patch.object(checkout_engine, "pinned_revision", return_value=self.revision), \
                mock.patch.object(checkout_engine.subprocess, "run") as run, \
                mock.patch.object(checkout_engine, "output", return_value="b" * 40):
            with self.assertRaisesRegex(ValueError, "differs from the pinned"):
                checkout_engine.checkout(destination)
            self.assertEqual(run.call_args_list[1].args[0][-1], self.revision)


class DockerContextTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.product = self.root / "afm"
        self.archives = self.root / "archives"
        self.archives.mkdir()
        self.destination = self.root / "context"
        for name in ("Dockerfile.runtime", "docker-entrypoint.sh", "scripts/telegram-notify.sh",
                     "contracts/adapters/install-cursor-agent.sh", "contracts/adapters/test-as-claude.sh",
                     "vendor/private-engine/secret.go", ".netrc", "go.mod"):
            path = self.product / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(name)
        for arch in ("amd64", "arm64"):
            with tarfile.open(self.archives / ("afm_linux_" + arch + ".tar.gz"), "w:gz") as archive:
                member = tarfile.TarInfo("afm")
                member.size = len(arch)
                archive.addfile(member, io.BytesIO(arch.encode()))
            for platform, suffix in (("darwin", ".tar.gz"), ("windows", ".zip")):
                (self.archives / ("afm_" + platform + "_" + arch + suffix)).write_bytes(b"other platform")
        self.write_manifest()

    def write_manifest(self):
        self.manifest = {"afm_commit": "reviewed-commit", "artifacts_sha256": {
            p.name: hashlib.sha256(p.read_bytes()).hexdigest()
            for p in self.archives.iterdir() if p.name != "build-manifest.json"
        }}
        (self.archives / "build-manifest.json").write_text(json.dumps(self.manifest))

    def prepare(self, commit="reviewed-commit"):
        with mock.patch.object(docker_context.subprocess, "check_output", return_value=commit):
            return docker_context.prepare(self.product, self.archives, self.destination, "v3.0.0")

    def test_context_contains_only_public_runtime_files_and_verified_binaries(self):
        self.assertEqual(self.prepare(), ["akopichin/afm:v3.0.0", "akopichin/afm:3", "akopichin/afm:latest"])
        files = {str(p.relative_to(self.destination)) for p in self.destination.rglob("*") if p.is_file()}
        self.assertEqual(files, {"Dockerfile.runtime", "docker-entrypoint.sh", "scripts/telegram-notify.sh",
                                "scripts/install-cursor-agent.sh", "scripts/test-as-claude.sh",
                                "binaries/linux_amd64/afm", "binaries/linux_arm64/afm"})
        for arch in ("amd64", "arm64"):
            binary = self.destination / "binaries" / ("linux_" + arch) / "afm"
            self.assertEqual(binary.read_text(), arch)
            self.assertEqual(binary.stat().st_mode & 0o777, 0o755)

    def test_wrong_commit_or_changed_archive_is_rejected_before_copying(self):
        with self.assertRaisesRegex(ValueError, "checked-out AFM commit"):
            self.prepare("different-commit")
        (self.archives / "afm_linux_arm64.tar.gz").write_bytes(b"tampered")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.prepare()
        self.assertFalse(self.destination.exists())

    def test_extra_archive_and_binary_symlink_are_rejected(self):
        (self.archives / "private-source.tar.gz").write_bytes(b"private")
        self.write_manifest()
        with self.assertRaisesRegex(ValueError, "six release archives"):
            self.prepare()
        (self.archives / "private-source.tar.gz").unlink()
        with tarfile.open(self.archives / "afm_linux_arm64.tar.gz", "w:gz") as archive:
            member = tarfile.TarInfo("afm")
            member.type = tarfile.SYMTYPE
            member.linkname = "../../private-source"
            archive.addfile(member)
        self.write_manifest()
        with self.assertRaisesRegex(ValueError, "regular file"):
            self.prepare()

    def test_public_context_file_cannot_be_a_symlink(self):
        path = self.product / "Dockerfile.runtime"
        path.unlink()
        path.symlink_to(self.product / ".netrc")
        with self.assertRaisesRegex(ValueError, "regular public file"):
            self.prepare()

    def test_channels_and_invalid_tags(self):
        for tag, channels in (("v1.2.18", ["1"]), ("v2.2.7", ["2"]), ("v3.0.1", ["3", "latest"]),
                              ("v3.1.0-alpha.1", []), ("v3.1.0-beta.2", [])):
            self.assertEqual(docker_context.image_tags(tag), ["akopichin/afm:" + x for x in [tag] + channels])
        for tag in ("", "main", "v3.0.0\ntags=other", "v3.0.0,other"):
            with self.assertRaises(ValueError):
                docker_context.image_tags(tag)


if __name__ == "__main__":
    unittest.main()
