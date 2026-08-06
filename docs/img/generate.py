#!/usr/bin/env python3
"""Generate the README's diagrams as SVG, in light and dark pairs.

The palette is lifted from docs/index.html so the README and the design
document look like one project rather than two.

Both variants come from one definition on purpose. Hand-maintaining a light
and a dark copy of the same picture guarantees they drift, and a diagram that
says something different depending on the reader's theme is worse than no
diagram.

    python3 docs/img/generate.py

Writes architecture-{light,dark}.svg and phase2-{light,dark}.svg beside this
script. The README references them through <picture>, so GitHub serves the
right one for the reader's theme.
"""

import pathlib

# Palette, from the :root blocks in docs/index.html.
LIGHT = dict(
    bg="#fbfaf7", panel="#ffffff", ink="#1c1a17", muted="#6b6459", faint="#938b7e",
    line="#e3ded4", accent="#7a5c2e", accent_soft="#f0e7d6",
    bad_bg="#fdf0ee", bad_line="#c4614c", bad_ink="#7a2f20",
)
DARK = dict(
    bg="#14130f", panel="#1b1a16", ink="#e8e3d8", muted="#a49b8b", faint="#7d7566",
    line="#302d26", accent="#d3ab6a", accent_soft="#2a2419",
    bad_bg="#251512", bad_line="#8c463a", bad_ink="#e8a595",
)

SANS = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif"
MONO = "ui-monospace,'SF Mono',SFMono-Regular,Menlo,Consolas,monospace"
SERIF = "'Iowan Old Style','Palatino Linotype',Palatino,Georgia,serif"


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def node(x, y, w, h, title, lines, p, accent=False):
    """A rounded box with a bold title and muted detail lines."""
    fill = p["accent_soft"] if accent else p["panel"]
    stroke = p["accent"] if accent else p["line"]
    out = [
        f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="7" '
        f'fill="{fill}" stroke="{stroke}" stroke-width="1.5"/>',
        f'<text x="{x + w/2}" y="{y + 24}" text-anchor="middle" font-family="{SANS}" '
        f'font-size="14" font-weight="650" fill="{p["ink"]}">{esc(title)}</text>',
    ]
    for i, line in enumerate(lines):
        out.append(
            f'<text x="{x + w/2}" y="{y + 43 + i*15}" text-anchor="middle" '
            f'font-family="{SANS}" font-size="11" fill="{p["muted"]}">{esc(line)}</text>'
        )
    return "\n  ".join(out)


def arrow(x1, y, x2, p, label="", dashed=False, mid=None):
    dash = ' stroke-dasharray="4 3"' if dashed else ""
    parts = [
        f'<line x1="{x1}" y1="{y}" x2="{x2 - 7}" y2="{y}" stroke="{p["muted"]}" '
        f'stroke-width="1.5"{dash}/>',
        f'<path d="M{x2-7} {y-4} L{x2} {y} L{x2-7} {y+4} Z" fill="{p["muted"]}"/>',
    ]
    if label:
        cx = mid if mid is not None else (x1 + x2) / 2
        parts.append(
            f'<text x="{cx}" y="{y - 9}" text-anchor="middle" font-family="{MONO}" '
            f'font-size="10" letter-spacing="0.06em" fill="{p["faint"]}">{esc(label)}</text>'
        )
    return "\n  ".join(parts)


def architecture(p):
    """The three-party split, and which half of your identity each one holds."""
    W, H = 960, 470
    # Four columns, sized so a gap always fits its label without touching a box.
    BW, GAP = 168, 96
    COL = [0, 264, 528, 792]
    body = []

    body.append(
        f'<text x="0" y="16" font-family="{MONO}" font-size="10.5" letter-spacing="0.1em" '
        f'fill="{p["accent"]}">the design</text>'
    )

    # The mint sits directly above the client it issues tokens to, so the
    # arrow can drop straight down instead of pointing vaguely into the row.
    body.append(node(COL[0], 40, BW, 78, "eregion · the mint", [
        "knows who paid", "never sees a prompt"], p, accent=True))
    body.append(
        f'<line x1="{COL[0]+BW/2}" y1="118" x2="{COL[0]+BW/2}" y2="183" stroke="{p["faint"]}" '
        f'stroke-width="1.3" stroke-dasharray="4 3"/>'
        f'<path d="M{COL[0]+BW/2-4} 176 L{COL[0]+BW/2} 183 L{COL[0]+BW/2+4} 176 Z" fill="{p["faint"]}"/>'
        f'<text x="{COL[0]+BW/2+14}" y="155" font-family="{MONO}" font-size="10" '
        f'letter-spacing="0.06em" fill="{p["faint"]}">blind-signed tokens</text>'
    )

    y, BH = 190, 84
    body.append(node(COL[0], y, BW, BH, "bearer", ["your machine", "holds your key"], p))
    body.append(node(COL[1], y, BW, BH, "ranger · relay", ["sees your address", "never your words"], p))
    body.append(node(COL[2], y, BW, BH, "mithlond · gateway", ["sees your words", "never who you are"], p))
    body.append(node(COL[3], y, BW, BH, "provider", ["Anthropic", "OpenAI"], p, accent=True))

    mid = y + BH / 2
    body.append(arrow(COL[0]+BW, mid, COL[1], p, "encrypted"))
    body.append(arrow(COL[1]+BW, mid, COL[2], p, "encrypted"))
    body.append(arrow(COL[2]+BW, mid, COL[3], p, "pooled key"))

    LEFT_END, RIGHT_START = COL[1] + BW, COL[2]
    body.append(
        f'<text x="0" y="322" font-family="{MONO}" font-size="10.5" letter-spacing="0.1em" '
        f'fill="{p["muted"]}">who knows what</text>'
        f'<line x1="0" y1="332" x2="{W}" y2="332" stroke="{p["line"]}" stroke-width="1"/>'
    )

    def span(x, w, yy, text, filled):
        if filled:
            box = (f'<rect x="{x}" y="{yy}" width="{w}" height="21" rx="4" '
                   f'fill="{p["accent_soft"]}" stroke="{p["accent"]}" stroke-width="1"/>')
            col = p["accent"]
        else:
            box = (f'<rect x="{x}" y="{yy}" width="{w}" height="21" rx="4" fill="none" '
                   f'stroke="{p["line"]}" stroke-dasharray="3 3"/>')
            col = p["faint"]
        return (box + f'<text x="{x + w/2}" y="{yy + 14.5}" text-anchor="middle" '
                f'font-family="{MONO}" font-size="10.5" fill="{col}">{esc(text)}</text>')

    body.append(span(0, LEFT_END, 352, "identity known here", True))
    body.append(span(RIGHT_START, W - RIGHT_START, 352, "identity unknown from here on", False))
    body.append(span(0, LEFT_END, 386, "content is opaque ciphertext", False))
    body.append(span(RIGHT_START, W - RIGHT_START, 386, "content readable, hence the TEE", True))

    body.append(
        f'<text x="0" y="437" font-family="{SANS}" font-size="11.5" fill="{p["faint"]}">'
        f'No party sits on both sides. The design rests on the mint, relay and gateway not colluding.'
        f'</text>'
    )
    return W, H, body


def phase2(p):
    """What Phase 2 actually is, and why there are two layers of TLS."""
    W, H = 960, 320
    BW = 168
    COL = [0, 264, 528, 792]
    body = []

    body.append(
        f'<text x="0" y="16" font-family="{MONO}" font-size="10.5" letter-spacing="0.1em" '
        f'fill="{p["accent"]}">built today · phase 2</text>'
    )

    y, BH = 118, 80
    body.append(node(COL[0], y, BW, BH, "your tool", ["SDK, editor, agent"], p))
    body.append(node(COL[1], y, BW, BH, "bearer", ["127.0.0.1:8080", "your own machine"], p))
    body.append(node(COL[2], y, BW, BH, "ranger", ["a VPS elsewhere"], p))
    body.append(node(COL[3], y, BW, BH, "provider", ["api.anthropic.com"], p, accent=True))

    mid = y + BH / 2
    body.append(arrow(COL[0]+BW, mid, COL[1], p, "plain http"))
    body.append(arrow(COL[1]+BW, mid, COL[2], p))
    body.append(arrow(COL[2]+BW, mid, COL[3], p))

    def bracket_above(x1, x2, yy, label, detail):
        return (
            f'<path d="M{x1} {yy+11} L{x1} {yy} L{x2} {yy} L{x2} {yy+11}" fill="none" '
            f'stroke="{p["accent"]}" stroke-width="1.3"/>'
            f'<text x="{(x1+x2)/2}" y="{yy-21}" text-anchor="middle" font-family="{MONO}" '
            f'font-size="10.5" letter-spacing="0.06em" fill="{p["accent"]}">{esc(label)}</text>'
            f'<text x="{(x1+x2)/2}" y="{yy-6}" text-anchor="middle" font-family="{SANS}" '
            f'font-size="11" fill="{p["muted"]}">{esc(detail)}</text>'
        )

    def bracket_below(x1, x2, yy, label, detail):
        return (
            f'<path d="M{x1} {yy-11} L{x1} {yy} L{x2} {yy} L{x2} {yy-11}" fill="none" '
            f'stroke="{p["accent"]}" stroke-width="1.3"/>'
            f'<text x="{(x1+x2)/2}" y="{yy+22}" text-anchor="middle" font-family="{MONO}" '
            f'font-size="10.5" letter-spacing="0.06em" fill="{p["accent"]}">{esc(label)}</text>'
            f'<text x="{(x1+x2)/2}" y="{yy+37}" text-anchor="middle" font-family="{SANS}" '
            f'font-size="11" fill="{p["muted"]}">{esc(detail)}</text>'
        )

    body.append(bracket_above(COL[1], COL[2]+BW, 78,
                              "tls #1", "hides which provider you use"))
    body.append(bracket_below(COL[1], COL[3]+BW, 228,
                              "tls #2", "end to end, so the relay holds no key for it"))

    body.append(
        f'<text x="0" y="305" font-family="{SANS}" font-size="11.5" fill="{p["faint"]}">'
        f'The plaintext hop never leaves your machine. eregion and mithlond are Phase 3 and are not built.'
        f'</text>'
    )
    return W, H, body


def render(builder, p, path):
    W, H, body = builder(p)
    pad = 18
    svg = (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{W + pad*2}" height="{H + pad*2}" '
        f'viewBox="0 0 {W + pad*2} {H + pad*2}" role="img">\n'
        f'  <rect width="100%" height="100%" fill="{p["bg"]}"/>\n'
        f'  <g transform="translate({pad},{pad})">\n  '
        + "\n  ".join(x for x in body if x)
        + "\n  </g>\n</svg>\n"
    )
    path.write_text(svg, encoding="utf-8")
    return len(svg)


def main():
    here = pathlib.Path(__file__).parent
    here.mkdir(parents=True, exist_ok=True)
    for name, builder in (("architecture", architecture), ("phase2", phase2)):
        for variant, palette in (("light", LIGHT), ("dark", DARK)):
            out = here / f"{name}-{variant}.svg"
            size = render(builder, palette, out)
            print(f"wrote {out.relative_to(here.parent.parent)} ({size} bytes)")


if __name__ == "__main__":
    main()
