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
        self.anchors = []
        self.metas = []
        self.text = []
        self.download_gates = []
        self.h1_count = 0
        self._in_download_checklist = False
        self._download_gate_parts = None

    def handle_starttag(self, tag, attrs):
        attributes = dict(attrs)
        self.tags.append(tag)
        if tag == "h1":
            self.h1_count += 1
        if tag == "a":
            self.anchors.append(attributes)
        if tag == "meta":
            self.metas.append(attributes)
        if tag == "ul" and "checklist" in attributes.get("class", "").split():
            self._in_download_checklist = True
        elif tag == "li" and self._in_download_checklist:
            self._download_gate_parts = []
        for name in ("href", "src"):
            if attributes.get(name):
                self.references.append((tag, name, attributes[name], attributes))

    def handle_endtag(self, tag):
        if tag == "li" and self._download_gate_parts is not None:
            text = " ".join("".join(self._download_gate_parts).split())
            self.download_gates.append(text)
            self._download_gate_parts = None
        elif tag == "ul" and self._in_download_checklist:
            self._in_download_checklist = False

    def handle_data(self, data):
        self.text.append(data)
        if self._download_gate_parts is not None:
            self._download_gate_parts.append(data)


class StaticSiteTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.index = SITE / "index.html"
        cls.paths = sorted(SITE.rglob("*.html"))
        cls.pages = {}
        cls.source = {}
        for path in cls.paths:
            source = path.read_text(encoding="utf-8")
            page = Page()
            page.feed(source)
            cls.pages[path] = page
            cls.source[path] = source
        cls.page = cls.pages[cls.index]

    def test_audits_every_served_html_document(self):
        self.assertTrue(self.paths, "the Pages document root has no HTML")
        self.assertEqual(
            set(self.paths),
            set(SITE.rglob("*.html")),
            "every HTML document served by Pages must be parsed",
        )
        for path, page in self.pages.items():
            with self.subTest(page=path.name):
                self.assertEqual(page.h1_count, 1)

    def test_served_pages_have_no_prompt_or_tracking_surface(self):
        forbidden = {"form", "iframe", "input", "script", "textarea"}
        for path, page in self.pages.items():
            with self.subTest(page=path.name):
                self.assertFalse(forbidden.intersection(page.tags))

    def test_assets_are_local_and_relative_links_exist_in_every_page(self):
        root = ROOT.resolve()
        for path, page in self.pages.items():
            for tag, name, reference, attributes in page.references:
                with self.subTest(page=path.name, reference=reference):
                    parsed = urlsplit(reference)
                    rel = attributes.get("rel", "").split()
                    is_asset = name == "src" or (tag == "link" and bool({"icon", "stylesheet", "preload"}.intersection(rel)))
                    if is_asset:
                        self.assertFalse(
                            parsed.scheme or parsed.netloc,
                            f"external {tag} asset is not allowed: {reference}",
                        )
                    if parsed.scheme or parsed.netloc or not parsed.path:
                        continue
                    target = (path.parent / unquote(parsed.path)).resolve()
                    self.assertTrue(target.is_relative_to(root), f"local link escapes the repository: {reference}")
                    self.assertTrue(target.exists(), f"missing local target for {reference}")

    def test_landing_page_does_not_send_referrers_to_external_links(self):
        policies = {
            meta.get("content", "").casefold()
            for meta in self.page.metas
            if meta.get("name", "").casefold() == "referrer"
        }
        self.assertIn("no-referrer", policies)
        for anchor in self.page.anchors:
            reference = anchor.get("href", "")
            parsed = urlsplit(reference)
            if not (parsed.scheme or parsed.netloc):
                continue
            with self.subTest(reference=reference):
                self.assertIn("noreferrer", anchor.get("rel", "").split())

    def test_beta_call_to_action_discloses_public_github_identity(self):
        text = " ".join("".join(self.page.text).split())
        self.assertIn("GitHub sign-in is required", text)
        self.assertIn("public issue linked to your GitHub username", text)

    def test_every_download_gate_has_visible_not_yet_text(self):
        self.assertGreater(len(self.page.download_gates), 0)
        for gate in self.page.download_gates:
            with self.subTest(gate=gate):
                self.assertTrue(gate.casefold().startswith("not yet:"), gate)

    def test_landing_page_uses_two_tone_keyboard_focus_indicator(self):
        source = self.source[self.index]
        self.assertIn("outline:3px solid var(--white)", source)
        self.assertIn("box-shadow:0 0 0 6px #00566b", source)

    def test_landing_page_avoids_absolute_current_privacy_claims(self):
        source = self.source[self.index]
        self.assertNotIn("an independently operated encrypted relay", source)
        self.assertNotIn("an unlinkable single-use token", source)

    def test_design_document_links_back_to_project_home(self):
        design = self.source[SITE / "design.html"]
        self.assertIn('href="./"', design)


if __name__ == "__main__":
    unittest.main()
