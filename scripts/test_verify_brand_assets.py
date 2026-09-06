#!/usr/bin/env python3
"""Tests for verify_brand_assets.py — run from repo root: python3 -m pytest scripts/test_verify_brand_assets.py
Self-contained (no pytest dependency: run with python3 scripts/test_verify_brand_assets.py)."""
import json, subprocess, sys, tempfile, unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
VALIDATOR = REPO_ROOT / "scripts" / "verify_brand_assets.py"


def run_validator(root: Path):
    return subprocess.run([sys.executable, str(VALIDATOR)], cwd=root,
                          capture_output=True, text=True)


class ValidatorTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        # minimal valid repo skeleton
        (self.root / ".github/assets/brand").mkdir(parents=True)
        (self.root / ".github/assets/github").mkdir(parents=True)
        (self.root / ".github/assets/architecture").mkdir(parents=True)
        (self.root / ".github/assets/screenshots").mkdir(parents=True)
        (self.root / "scripts").mkdir()
        (self.root / "README.md").write_text("<!-- aftergraph-brand-os:v1.0.0 -->\n")
        (self.root / "brand-assets.json").write_text(json.dumps({
            "repo": "test", "required": [
                ".github/assets/brand/logo.svg",
                ".github/assets/brand/logo-dark.svg",
                ".github/assets/brand/logo-light.svg",
                ".github/assets/github/hero.webp",
                ".github/assets/github/social-preview.png",
            ],
            "architectureFamily": [".github/assets/architecture/system-context.svg"],
        }))
        # minimal assets
        (self.root / ".github/assets/brand/logo.svg").write_text(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>')
        (self.root / ".github/assets/brand/logo-dark.svg").write_text(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>')
        (self.root / ".github/assets/brand/logo-light.svg").write_text(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>')
        (self.root / ".github/assets/architecture/system-context.svg").write_text(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>')
        # 1280x640 PNG (minimal valid)
        import struct, zlib
        def make_png(w, h, path):
            sig = b"\x89PNG\r\n\x1a\n"
            ihdr = struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)
            def chunk(t, d):
                c = t + d
                return struct.pack(">I", len(d)) + c + struct.pack(">I", zlib.crc32(c))
            raw = b"".join(b"\x00" + b"\x80\x80\x80" * w for _ in range(h))
            png = sig + chunk(b"IHDR", ihdr) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")
            path.write_bytes(png)
        make_png(1280, 640, self.root / ".github/assets/github/social-preview.png")
        # tiny webp stub (just RIFF header enough for size skip; validator only checks png screenshots)
        (self.root / ".github/assets/github/hero.webp").write_bytes(b"RIFF\x10\x00\x00\x00WEBPVP8 ")
        self.validator = VALIDATOR

    def tearDown(self):
        self.tmp.cleanup()

    def test_happy_path(self):
        r = run_validator(self.root)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("OK:", r.stdout)

    def test_missing_required_fails(self):
        (self.root / ".github/assets/brand/logo.svg").unlink()
        r = run_validator(self.root)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("missing required", r.stdout)

    def test_bad_svg_fails(self):
        (self.root / ".github/assets/architecture/system-context.svg").write_text("not svg")
        r = run_validator(self.root)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("invalid SVG", r.stdout)

    def test_readme_broken_ref_fails(self):
        readme = self.root / "README.md"
        readme.write_text('<!-- aftergraph-brand-os:v1.0.0 -->\n<img src=".github/assets/github/hero.webp">\n')
        (self.root / ".github/assets/github/hero.webp").unlink()
        r = run_validator(self.root)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("broken image ref", r.stdout)

    def test_placeholder_marker_fails(self):
        (self.root / "brand-assets.json").write_text(json.dumps({
            "repo": "test",
            "required": [".github/assets/brand/logo.svg"],
            "architectureFamily": [".github/assets/architecture/system-context.svg"],
        }))
        (self.root / ".github/assets/brand/logo.svg").write_text("<svg>placeholder text</svg>")
        r = run_validator(self.root)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("placeholder marker", r.stdout)

    def test_missing_dark_light_fails(self):
        (self.root / ".github/assets/brand/logo-dark.svg").unlink()
        r = run_validator(self.root)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("logo-dark.svg", r.stdout)

    def test_duplicate_registry_ids_fail(self):
        (self.root / "brand-registry.json").write_text(json.dumps({
            "assets": [{"id": "x"}, {"id": "x"}]
        }))
        r = run_validator(self.root)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("duplicate asset ids", r.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)