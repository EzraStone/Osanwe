import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
FORMS = ROOT / ".github" / "ISSUE_TEMPLATE"


class BetaFormTest(unittest.TestCase):
    def setUp(self):
        self.interest = (FORMS / "beta-interest.yml").read_text(encoding="utf-8")
        self.report = (FORMS / "beta-report.yml").read_text(encoding="utf-8")

    def test_both_public_forms_disclose_github_identity_linkage(self):
        for name, form in {"interest": self.interest, "report": self.report}.items():
            with self.subTest(form=name):
                self.assertIn("GitHub sign-in is required", form)
                self.assertIn("public issue linked to your GitHub username", form)
                self.assertIn("required: true", form)

    def test_both_forms_disclose_provider_content_access(self):
        for name, form in {"interest": self.interest, "report": self.report}.items():
            with self.subTest(form=name):
                self.assertIn("model provider", form)
                self.assertIn("prompt", form)
                self.assertIn("answer", form)
                self.assertIn("gateway account", form)

    def test_forms_direct_secrets_and_vulnerabilities_away_from_public_issues(self):
        for name, form in {"interest": self.interest, "report": self.report}.items():
            with self.subTest(form=name):
                for secret in ("relay secret", "entitlement", "API key"):
                    self.assertIn(secret, form)
        self.assertIn("Security tab", self.report)


if __name__ == "__main__":
    unittest.main()
