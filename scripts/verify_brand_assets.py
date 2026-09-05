#!/usr/bin/env python3
from pathlib import Path
from xml.etree import ElementTree as ET
import json, struct

ROOT=Path(__file__).resolve().parents[1]
data=json.loads((ROOT/"brand-assets.json").read_text(encoding="utf-8"))
errors=[]

def png_size(path):
    with path.open("rb") as f: b=f.read(24)
    if b[:8]!=b"\x89PNG\r\n\x1a\n": raise ValueError("not PNG")
    return struct.unpack(">II",b[16:24])

for rel in data["required"]:
    if not (ROOT/rel).exists(): errors.append(f"missing: {rel}")
for rel in data["architectureFamily"]:
    p=ROOT/rel
    if not p.exists(): errors.append(f"missing architecture asset: {rel}")
    else:
        try: ET.parse(p)
        except Exception as e: errors.append(f"invalid SVG {rel}: {e}")

sp=ROOT/".github/assets/github/social-preview.png"
if sp.exists():
    if png_size(sp)!=(1280,640): errors.append(f"social preview wrong size: {png_size(sp)}")
    if sp.stat().st_size>=1_000_000: errors.append(f"social preview >=1MB: {sp.stat().st_size}")

readme=ROOT/"README.md"
if readme.exists() and "<!-- aftergraph-brand-os:v1.0.0 -->" not in readme.read_text(encoding="utf-8"):
    errors.append("README.md missing Brand OS marker")

if errors:
    print("\n".join(errors)); raise SystemExit(1)
print(f"OK: {data['repo']} satisfies aftergraph.brand-assets/1.0")
