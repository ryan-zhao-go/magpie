from pathlib import Path
import re
import subprocess
import tempfile
import unittest


workflow = Path(__file__).resolve().parents[1] / ".github/workflows/fork-release.yml"


class ReleaseWorkflowTests(unittest.TestCase):
    def test_verification_never_publishes(self):
        source = workflow.read_text()
        self.assertIn("verify_only:", source)
        for name in (
            "Preserve the exact tested source as a tag",
            "Publish fork release",
            "Publish latest verified dev artifact",
        ):
            block = source.split(f"- name: {name}\n", 1)[1].split("\n      - ", 1)[0]
            self.assertIn("inputs.verify_only != true", block)

    def test_manual_run_checks_its_own_branch(self):
        source = workflow.read_text()
        self.assertIn("github.ref_name", source)
        self.assertIn('git fetch --force --tags upstream "$UPSTREAM_REF"', source)

    def test_merge_preserves_resolved_patch_history(self):
        source = workflow.read_text()
        self.assertIn('git merge --no-edit "$UPSTREAM_SHA"', source)
        self.assertNotRegex(source, r"\bgit rebase\b")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def git(*arguments, check=True):
                return subprocess.run(
                    ["git", "-C", directory, *arguments],
                    capture_output=True, text=True, check=check,
                )

            def commit(content, message):
                (root / "fields").write_text(content)
                git("add", "fields")
                git("commit", "-m", message)
                return git("rev-parse", "HEAD").stdout.strip()

            git("init", "-b", "base")
            git("config", "user.name", "Release Test")
            git("config", "user.email", "release-test@example.invalid")
            git("config", "core.hooksPath", "/dev/null")
            commit("firstWait\n", "base")
            git("switch", "-c", "patch")
            old_patch = commit("firstWait\nanswerWait\n", "answer timeout")
            git("switch", "-c", "upstream", "base")
            commit("firstWait\nbudgeted\n", "total budget")
            git("switch", "patch")
            self.assertNotEqual(git("merge", "upstream", check=False).returncode, 0)
            resolved = "firstWait\nbudgeted\nanswerWait\n"
            commit(resolved, "retain both behaviors")
            git("switch", "upstream")
            (root / "unrelated").write_text("next upstream change\n")
            git("add", "unrelated")
            git("commit", "-m", "upstream continues")
            git("switch", "patch")
            command = re.search(r'git merge --no-edit "\$UPSTREAM_SHA"', source).group()
            command = command.replace('"$UPSTREAM_SHA"', "upstream")
            subprocess.run(command.split()[:1] + ["-C", directory] + command.split()[1:], check=True, capture_output=True)
            self.assertEqual((root / "fields").read_text(), resolved)
            self.assertEqual(git("merge-base", "--is-ancestor", old_patch, "HEAD").returncode, 0)
            git("merge", "--no-edit", "upstream")


if __name__ == "__main__":
    unittest.main()
