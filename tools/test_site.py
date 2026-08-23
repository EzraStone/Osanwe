from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit
import unittest


ROOT = Path(__file__).resolve().parents[1]
SITE = ROOT / "docs"


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.tags = []
        self.references = []
        self.h1_count = 0

    def handle_starttag(self, tag, attrs):
        attributes = dict(attrs)
        self.tags.append(tag)
        if tag == "h1":
            self.h1_count += 1
        for name in ("href", "src"):
            if attributes.get(name):
                self.references.append((tag, name, attributes[name]))


class StaticSiteTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.index = SITE / "index.html"
        cls.page = Page()
        cls.page.feed(cls.index.read_text(encoding="utf-8"))

    def test_has_one_primary_heading(self):
        self.assertEqual(self.page.h1_count, 1)

    def test_public_page_has_no_prompt_or_tracking_surface(self):
        forbidden = {"form", "iframe", "input", "script", "textarea"}
        self.assertFalse(forbidden.intersection(self.page.tags))

    def test_assets_are_local_and_relative_links_exist(self):
        for tag, name, reference in self.page.references:
            parsed = urlsplit(reference)
            if name == "src":
                self.assertFalse(
                    parsed.scheme or parsed.netloc,
                    f"external {tag} asset is not allowed: {reference}",
                )
            if parsed.scheme or parsed.netloc or not parsed.path:
                continue
            target = (self.index.parent / unquote(parsed.path)).resolve()
            self.assertTrue(target.exists(), f"missing local target for {reference}")

    def test_design_document_links_back_to_project_home(self):
        design = (SITE / "design.html").read_text(encoding="utf-8")
        self.assertIn('href="./"', design)


if __name__ == "__main__":
    unittest.main()
