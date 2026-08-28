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

    def test_served_pages_do_not_send_referrers(self):
        for path, page in self.pages.items():
            policies = {
                meta.get("content", "").casefold()
                for meta in page.metas
                if meta.get("name", "").casefold() == "referrer"
            }
            with self.subTest(page=path.name):
                self.assertIn("no-referrer", policies)

        # The landing page repeats the policy per external link so a future
        # templating or proxy mistake cannot weaken its main calls to action.
        for anchor in self.page.anchors:
            reference = anchor.get("href", "")
            parsed = urlsplit(reference)
            if not (parsed.scheme or parsed.netloc):
                continue
            with self.subTest(reference=reference):
                self.assertIn("noreferrer", anchor.get("rel", "").split())

    def test_removed_recruitment_and_evidence_sections_stay_absent(self):
        text = " ".join("".join(self.page.text).split())
        for phrase in (
            "A test cohort, not an audience.",
            "Phase 0 currently points toward async work.",
            "Download locally. Keep the public page out of the conversation.",
            "Read the beta charter",
        ):
            with self.subTest(phrase=phrase):
                self.assertNotIn(phrase, text)

    def test_landing_page_uses_two_tone_keyboard_focus_indicator(self):
        source = self.source[self.index]
        self.assertIn("outline:3px solid var(--white)", source)
        self.assertIn("box-shadow:0 0 0 6px #00566b", source)

    def test_landing_page_avoids_absolute_current_privacy_claims(self):
        source = self.source[self.index]
        self.assertNotIn("an independently operated encrypted relay", source)
        self.assertNotIn("an unlinkable single-use token", source)

    def test_landing_page_preserves_dignity_and_product_boundaries(self):
        source = self.source[self.index]
        text = " ".join("".join(self.page.text).split())
        for phrase in (
            "Humans deserve to maintain their dignity when using our greatest invention.",
            "Technology should serve the human first.",
            "You are a person, not a profile.",
            "Catholic in inspiration",
            "Real local client interface",
            "Interactive HTML requires Chromium 152+",
            "Cowork",
            "What are you thinking about?",
        ):
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, text)
        self.assertNotIn("Humans deserve to preserve their dignity", text)
        self.assertIn(".hero-plate.shell{width:100%;height:100svh", source)
        self.assertIn('href="http://127.0.0.1:8080/_osanwe/"', source)
        self.assertIn('role="img"', source)
        self.assertNotIn("Generated locally", source)

    def test_design_document_links_back_to_project_home(self):
        design = self.source[SITE / "design.html"]
        self.assertIn('href="./"', design)


if __name__ == "__main__":
    unittest.main()
