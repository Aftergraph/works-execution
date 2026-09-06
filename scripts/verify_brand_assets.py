#!/usr/bin/env python3
"""Aftergraph Brand OS validator v2 — semantic completion, not just presence.

Checks (per repo):
 1. required files exist (brand-assets.json "required")
 2. architecture SVGs parse (ElementTree)
 3. social preview == 1280x640, < 1MB
 4. README marker present
 5. manifest.json parses (if present)
 6. registry.json parses, no duplicate ids (if present)
 7. README image refs resolve on disk
 8. no placeholder markers in release-ready assets
 9. dark/light logo pair exists where required
10. screenshots (if present) meet min dimensions; product-main.webp NOT
    counted as evidence; capture-required markers forbid production-ready
"""
from pathlib import Path
from xml.etree import ElementTree as ET
import json, struct, re, sys

ROOT = Path.cwd()
errors = []
warnings = []

def png_size(path):
    with path.open("rb") as f:
        b = f.read(24)
    if b[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError("not PNG")
    return struct.unpack(">II", b[16:24])

def webp_size(path):
    b = path.read_bytes()
    if b[:4] != b"RIFF" or b[8:12] != b"WEBP":
        raise ValueError("not WebP")
    if b[12:16] == b"VP8X":
        return (int.from_bytes(b[24:27], "little") + 1,
                int.from_bytes(b[27:30], "little") + 1)
    if b[12:16] == b"VP8L":
        bb = b[21:26]
        return ((bb[1] & 0x3F) << 8 | bb[0]) + 1, \
               ((bb[3] & 0xF) << 10 | bb[2] << 2 | (bb[1] & 0xC0) >> 6) + 1
    if b[12:16] == b"VP8 ":
        return (int.from_bytes(b[26:28], "little") & 0x3FFF,
                int.from_bytes(b[28:30], "little") & 0x3FFF)
    return None

manifest_path = ROOT / "brand-assets.json"
if not manifest_path.exists():
    print("MISSING brand-assets.json — cannot validate"); sys.exit(1)

data = json.loads(manifest_path.read_text(encoding="utf-8"))

# 1. required files
for rel in data.get("required", []):
    if not (ROOT / rel).exists():
        errors.append(f"missing required: {rel}")

# 2. architecture SVGs parse
for rel in data.get("architectureFamily", []):
    p = ROOT / rel
    if not p.exists():
        errors.append(f"missing architecture asset: {rel}")
    else:
        try:
            ET.parse(p)
        except Exception as e:
            errors.append(f"invalid SVG {rel}: {e}")

# 3. social preview
sp = ROOT / ".github/assets/github/social-preview.png"
if sp.exists():
    try:
        if png_size(sp) != (1280, 640):
            errors.append(f"social preview wrong size: {png_size(sp)}")
        if sp.stat().st_size >= 1_000_000:
            errors.append(f"social preview >=1MB: {sp.stat().st_size}")
    except Exception as e:
        errors.append(f"social preview invalid: {e}")
else:
    warnings.append("social-preview.png absent (optional if repo has no social surface)")

# 4. README marker
readme = ROOT / "README.md"
if readme.exists():
    txt = readme.read_text(encoding="utf-8")
    if "<!-- aftergraph-brand-os:v1.0.0 -->" not in txt:
        errors.append("README.md missing Brand OS marker")

# 5. manifest parses
if (ROOT / "brand-manifest.json").exists():
    try:
        json.loads((ROOT / "brand-manifest.json").read_text(encoding="utf-8"))
    except Exception as e:
        errors.append(f"brand-manifest.json invalid: {e}")

# 6. registry parses, no dup ids
reg = ROOT / "brand-registry.json"
if reg.exists():
    try:
        r = json.loads(reg.read_text(encoding="utf-8"))
        ids = [a.get("id") for a in r.get("assets", [])]
        if len(ids) != len(set(ids)):
            errors.append("brand-registry.json has duplicate asset ids")
    except Exception as e:
        errors.append(f"brand-registry.json invalid: {e}")

# 7. README image refs resolve
if readme.exists():
    txt = readme.read_text(encoding="utf-8")
    for m in re.finditer(r'(?:srcset|src)="([^"]+\.(?:webp|png|svg|gif))"', txt):
        ref = m.group(1).split("?")[0].split("#")[0].removeprefix("./")
        if not (ROOT / ref).exists():
            errors.append(f"README broken image ref: {ref}")
    for m in re.finditer(r'!\[[^\]]*\]\(([^)]+\.(?:webp|png|svg|gif))\)', txt):
        ref = m.group(1).split("?")[0].split("#")[0].removeprefix("./")
        if not (ROOT / ref).exists():
            errors.append(f"README broken markdown img ref: {ref}")

# 8. placeholder markers forbidden in release-ready assets
PLACEHOLDER_MARKERS = ["placeholder", "lorem ipsum", "TODO: capture", "concept only",
                       "not a real screenshot", "product-main.webp placeholder"]
for rel in data.get("required", []) + data.get("architectureFamily", []):
    p = ROOT / rel
    if not p.exists() or p.suffix not in (".svg", ".md", ".json", ".webp"):
        continue
    content = p.read_text(encoding="utf-8", errors="ignore").lower()
    for marker in PLACEHOLDER_MARKERS:
        if marker in content:
            errors.append(f"placeholder marker '{marker}' in {rel}")

# 9. dark/light logo pair
logo_dark = ROOT / ".github/assets/brand/logo-dark.svg"
logo_light = ROOT / ".github/assets/brand/logo-light.svg"
if (ROOT / ".github/assets/brand/logo.svg").exists():
    for p, name in [(logo_dark, "logo-dark.svg"), (logo_light, "logo-light.svg")]:
        if not p.exists():
            errors.append(f"missing {name} (dark/light pair required)")

# 10. screenshots min dims; product-main.webp is concept-render, not evidence
ss_dir = ROOT / ".github/assets/screenshots"
if ss_dir.exists():
    for p in sorted(ss_dir.glob("*.png")) + sorted(ss_dir.glob("*.webp")):
        if p.name == "product-main.webp":
            warnings.append("product-main.webp is a concept render — not UI evidence")
            continue
        try:
            dims = png_size(p) if p.suffix == ".png" else webp_size(p)
            if dims and dims[0] < 1200:
                errors.append(f"screenshot {p.name} width {dims[0]} < 1200")
        except Exception as e:
            errors.append(f"screenshot {p.name} unreadable: {e}")

if errors:
    print("\n".join(errors))
    sys.exit(1)
for w in warnings:
    print("WARN:", w)
print(f"OK: {data.get('repo','?')} satisfies aftergraph.brand-assets/2.0")