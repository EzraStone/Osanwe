from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"
PINNED_ACTION = re.compile(r"^actions/[a-z0-9-]+@[0-9a-f]{40}$")


class WorkflowPolicyTest(unittest.TestCase):
    def test_every_action_is_first_party_and_immutably_pinned(self):
        found = 0
        for path in sorted(WORKFLOWS.glob("*.yml")):
            for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
                match = re.search(r"\buses:\s*([^\s#]+)", line)
                if not match:
                    continue
                found += 1
                reference = match.group(1)
                self.assertRegex(
                    reference,
                    PINNED_ACTION,
                    f"{path.relative_to(ROOT)}:{number} must pin an official action to a full commit",
                )
        self.assertGreater(found, 0)


if __name__ == "__main__":
    unittest.main()
