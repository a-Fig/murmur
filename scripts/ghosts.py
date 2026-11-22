"""Writes Murmur's ghost avatars (#579): 16 ghosts and 15 accessories, 64x64 SVGs.

A session's avatar is three layers: a round tile the page paints, a ghost, and an accessory
or none: 16 x 16 = 256 faces. The ghosts carry no tile, so it can take the session's device
colour (#571) or any tone, and their bodies are light tints that read on all twelve device
hues. Every body shares one head and one pair of eye positions, so each accessory fits every
ghost. The script also writes docs/ghosts.html, a sheet of every combination. The page picks
from GHOSTS and GHOST_ACCESSORIES in messages.js, which list these names in this order.

Usage: python scripts/ghosts.py
"""

import math
import os
import shutil

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "internal", "web", "static", "assets", "ghosts")
SHEET = os.path.join(ROOT, "docs", "ghosts.html")

# The brand's four colours (docs/brand.md), and a few more tile colours at the same weight.
BLUE = "#225BFF"
BUTTER = "#FDEE8C"
CREAM = "#FFFBEA"
INK = "#0E1B45"
CORAL = "#EE7F6C"
ROSE = "#E27D93"
MINT = "#4FAE8C"
PINK = "#F2A0B0"  # tongues and blush

# Body tints: the brand's light steps, and two more at the same lightness.
BUTTER_300 = "#FEF6C4"
BLUE_100 = "#DCE6FF"
MINT_100 = "#DDF3E8"
ROSE_100 = "#FCE1E6"

# The head every body shares: a dome of radius 19 centred on (32, 28), top at y 9, sides at x 13 and 51.
HEAD = "M13 46V28C13 17.5 21.5 9 32 9C42.5 9 51 17.5 51 28"

BODIES = {
    # Three round scallops, the first ghost's shape.
    "classic": HEAD + "V46C50.2 53.2 39.1 53.2 38.33 46C37.5 53.2 26.5 53.2 25.67 46C24.9 53.2 13.8 53.2 13 46Z",
    # Four smaller scallops.
    "frilly": HEAD + "V46C50.4 51.4 42.1 51.4 41.5 46C40.9 51.4 32.6 51.4 32 46"
    "C31.4 51.4 23.1 51.4 22.5 46C21.9 51.4 13.6 51.4 13 46Z",
    # A tail that flicks out to the right, as if mid-swoop.
    "swoosh": HEAD + "V38C51 45 54 50 58 52C52.5 55.5 45 54.5 41 50C37 54 29.5 55 23.5 51.5C17.5 48 13 44 13 38Z",
    # The classic shape with one arm up in a wave.
    "waving": HEAD + "V46C50.2 53.2 39.1 53.2 38.33 46C37.5 53.2 26.5 53.2 25.67 46C24.9 53.2 13.8 53.2 13 46Z",
}
ARMS = {
    "waving": '<path d="M14.5 36C10 34 7 30 6.6 26C6.3 23.6 8.8 22.6 10.2 24.4C11.8 26.6 13.6 29 16 30.4Z" fill="{body}"/>'
    '<path d="M49.5 34.5C53.5 36.5 55.5 39.5 55.8 42.3C56 44.3 54 45 52.7 43.7C51.4 42.3 50 40.8 48.5 40Z" fill="{body}"/>',
}

EL, ER, EY = 25.5, 38.5, 28  # eye centres


def eye(x, f, rx=3, ry=3.9, dx=0):
    return f'<ellipse cx="{x + dx}" cy="{EY}" rx="{rx}" ry="{ry}" fill="{f}"/>'


def eyes(f, **kw):
    return eye(EL, f, **kw) + eye(ER, f, **kw)


def arc(x, f, up):
    # A closed eye: up is a happy ^, down a content or sleepy U.
    d = -2.9 if up else 2.9
    y = EY + (1.2 if up else -0.4)
    return f'<path d="M{x - 2.9} {y}Q{x} {y + d} {x + 2.9} {y}" stroke="{f}" stroke-width="1.8" stroke-linecap="round" fill="none"/>'


def mouth(d, f, w=1.7):
    return f'<path d="{d}" stroke="{f}" stroke-width="{w}" stroke-linecap="round" stroke-linejoin="round" fill="none"/>'


# The mark's eyes (docs/assets/brand/mark.svg), cut from the designer's paths: the right
# agent's slant like a quotation mark, the left agent's are leaning ovals.
QUOTE_EYE = (
    "M129.5 49.07C127.85 48.68 124.24 50.55 123.44 53.89C122.36 58.41 122.53 61.12 126.16 61.42"
    "C129.06 61.42 129.21 60.05 129.6 57.12C129.72 56.17 130.02 55.26 130.36 54.38C131.42 51.68 131.59 49.57 129.5 49.07Z"
)
OVAL_EYE = "M47.56 30.83C44.59 30.26 41.51 33.35 40.67 37.72C39.84 42.09 41.56 46.09 44.53 46.66C47.49 47.23 50.58 44.14 51.41 39.77C52.25 35.4 50.52 31.39 47.56 30.83Z"


def mark_eyes(path, cx, cy, s, f):
    return "".join(
        f'<path d="{path}" fill="{f}" transform="translate({x} {EY}) scale({s}) translate({-cx} {-cy})"/>' for x in (EL, ER)
    )


def star(x, f):
    pts = []
    for i in range(10):
        r = 4.4 if i % 2 == 0 else 1.9
        a = math.pi / 2 + i * math.pi / 5
        pts.append(f"{x + r * math.cos(a):.2f} {EY - r * math.sin(a):.2f}")
    return f'<path d="M{"L".join(pts)}Z" fill="{BUTTER}" stroke="{f}" stroke-width="1.1" stroke-linejoin="round"/>'


def sparkle(f):
    return eyes(f, rx=3.4, ry=4.3) + "".join(
        f'<circle cx="{x + 1.1}" cy="{EY - 1.5}" r="1.15" fill="#fff"/><circle cx="{x - 1}" cy="{EY + 1.6}" r=".55" fill="#fff"/>'
        for x in (EL, ER)
    )


def smug(f, body):
    lids = "".join(
        f'<rect x="{x - 4}" y="{EY - 5}" width="8" height="4.6" fill="{body}"/>'
        f'<path d="M{x - 3.4} {EY - 0.4}H{x + 3.4}" stroke="{f}" stroke-width="1.7" stroke-linecap="round"/>'
        for x in (EL, ER)
    )
    return eyes(f, dx=0.8) + lids + mouth("M29 36.2Q33.5 37.6 36.5 34.6", f)


def blush():
    return f'<ellipse cx="19.8" cy="34" rx="3" ry="2" fill="{PINK}" opacity=".85"/><ellipse cx="44.2" cy="34" rx="3" ry="2" fill="{PINK}" opacity=".85"/>'


FACES = {
    "quote": lambda f, b: mark_eyes(QUOTE_EYE, 127, 55.2, 0.66, f),
    "oval": lambda f, b: mark_eyes(OVAL_EYE, 46, 38.7, 0.52, f) + mouth("M29.6 35.6Q32 37.4 34.4 35.6", f),
    "sleepy": lambda f, b: arc(EL, f, False) + arc(ER, f, False) + f'<ellipse cx="32" cy="36" rx="1.3" ry="1" fill="{f}"/>',
    "side": lambda f, b: eyes(f, rx=2.6, ry=3.6, dx=2)
    + "".join(f'<path d="M{x - 1.5} {EY - 5.6}L{x + 3.6} {EY - 4.6}" stroke="{f}" stroke-width="1.5" stroke-linecap="round"/>' for x in (EL, ER))
    + mouth("M32.5 36H36", f),
    "open": lambda f, b: arc(EL, f, True) + arc(ER, f, True)
    + f'<path d="M27.6 33.6H36.4C36.4 37.8 34.4 39.6 32 39.6C29.6 39.6 27.6 37.8 27.6 33.6Z" fill="{f}"/>'
    f'<path d="M29.4 38.2C30.6 36.8 33.4 36.8 34.6 38.2C33.9 39.1 33 39.6 32 39.6C31 39.6 30.1 39.1 29.4 38.2Z" fill="{PINK}"/>',
    "blep": lambda f, b: arc(EL, f, True) + arc(ER, f, True) + mouth("M28 34.4Q32 37.4 36 34.4", f)
    + f'<path d="M30.8 35.9H35V38.4C35 40 33.9 41 32.9 41C31.9 41 30.8 40 30.8 38.4Z" fill="{PINK}"/>',
    "smug": smug,
    "sparkle": lambda f, b: sparkle(f) + mouth("M29.8 35.6Q32 37.2 34.2 35.6", f),
    "happy": lambda f, b: arc(EL, f, True) + arc(ER, f, True) + blush() + mouth("M29.4 34.8Q32 37.8 34.6 34.8", f),
    "shy": lambda f, b: eyes(f, rx=2.2, ry=2.8) + blush() + mouth("M29.2 35.4Q30.6 37 32 35.4Q33.4 37 34.8 35.4", f, 1.5),
    "content": lambda f, b: arc(EL, f, False) + arc(ER, f, False) + mouth("M28.6 34.6Q32 38 35.4 34.6", f),
    "wink": lambda f, b: eye(EL, f) + arc(ER, f, True) + mouth("M29 35Q32 37.8 35 35", f),
    "oh": lambda f, b: eyes(f) + f'<ellipse cx="32" cy="36.8" rx="2.1" ry="2.6" fill="{f}"/>',
    "stars": lambda f, b: star(EL, f) + star(ER, f) + mouth("M28.8 35.4Q32 38.4 35.2 35.4", f),
    "plain": lambda f, b: eyes(f),
}

# The ghosts, named for the ways people talk quietly. (body, shape, face)
GHOSTS = {
    "murmur": (CREAM, "classic", "quote"),
    "hush": (CREAM, "classic", "sleepy"),
    "whisper": (BLUE_100, "swoosh", "side"),
    "mumble": (BUTTER_300, "frilly", "oval"),
    "chatter": (CREAM, "waving", "open"),
    "babble": (MINT_100, "frilly", "blep"),
    "rumor": (CREAM, "swoosh", "smug"),
    "echo": (BLUE_100, "classic", "sparkle"),
    "chirp": (BUTTER, "waving", "happy"),
    "coo": (ROSE_100, "frilly", "shy"),
    "hum": (BUTTER, "swoosh", "content"),
    "psst": (CREAM, "classic", "wink"),
    "sigh": (CREAM, "frilly", "oh"),
    "jabber": (BUTTER_300, "waving", "stars"),
    "natter": (MINT_100, "classic", "plain"),
    "buzz": (ROSE_100, "waving", "quote"),
}

# The page's twelve device hues (#571, style.css), which the sheet uses for tiles.
DEVICE_HUES = [
    "oklch(0.57 0.085 15)", "oklch(0.62 0.09 45)", "oklch(0.66 0.09 82)", "oklch(0.6 0.08 115)",
    "oklch(0.57 0.075 145)", "oklch(0.62 0.075 180)", "oklch(0.57 0.07 210)", "oklch(0.6 0.075 238)",
    "oklch(0.56 0.09 290)", "oklch(0.6 0.09 318)", "oklch(0.55 0.09 348)", "oklch(0.5 0.05 60)",
]


def ghost(name):
    body, shape, face = GHOSTS[name]
    d = BODIES[shape]
    arms = ARMS.get(shape, "").format(body=body)
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">'
        f'<defs><clipPath id="g"><path d="{d}"/></clipPath></defs>'
        '<ellipse cx="33" cy="57.5" rx="13.5" ry="2" fill="#000" opacity=".16"/>'
        f'{arms}<path d="{d}" fill="{body}"/>'
        # Shade: the body, minus itself nudged up and left, leaves a crescent on the right
        # and along the scallops that follows any shape.
        f'<g clip-path="url(#g)"><rect width="64" height="64" fill="{INK}" opacity=".09"/>'
        f'<path d="{d}" fill="{body}" transform="translate(-4 -1.6)"/></g>'
        f"{FACES[face](INK, body)}</svg>\n"
    )


def petals(cx, cy, r, n, fill, rot=0):
    out = ""
    for i in range(n):
        a = rot + i * 360 / n
        x = cx + r * math.cos(math.radians(a))
        y = cy + r * math.sin(math.radians(a))
        out += f'<circle cx="{x:.2f}" cy="{y:.2f}" r="{r * 0.95:.2f}" fill="{fill}"/>'
    return out


LINE = f'stroke="{INK}" stroke-width="1.3" stroke-linejoin="round"'

# Each sits on the shared head; none reaches past the 64x64 box.
ACCESSORIES = {
    "bow": f'<g fill="{CORAL}" {LINE}><path d="M42.4 12.6L36.6 8.8C35.8 8.3 34.8 8.9 34.9 9.8L35.5 15.6C35.6 16.4 36.5 16.8 37.2 16.4Z"/>'
    f'<path d="M42.4 12.6L48.8 10C49.7 9.6 50.6 10.4 50.3 11.3L48.5 16.8C48.2 17.6 47.2 17.8 46.6 17.3Z"/>'
    f'<circle cx="42.4" cy="12.8" r="2.4"/></g>',
    "glasses": f'<g stroke="{INK}" stroke-width="1.6" fill="{CREAM}" fill-opacity=".28"><rect x="19.6" y="23" width="11.8" height="10" rx="2.6"/>'
    f'<rect x="32.6" y="23" width="11.8" height="10" rx="2.6"/><path d="M31.4 27H32.6M19.6 26.4L13.4 25.2M44.4 26.4L50.6 25.2" fill="none"/></g>',
    "specs": f'<g stroke="{CORAL}" stroke-width="1.7" fill="{CREAM}" fill-opacity=".28"><circle cx="{EL}" cy="{EY}" r="5.6"/>'
    f'<circle cx="{ER}" cy="{EY}" r="5.6"/><path d="M30.9 27.2Q32 26.2 33.1 27.2M19.9 27.4L13.4 26.4M44.1 27.4L50.6 26.4" fill="none"/></g>',
    "shades": f'<g fill="{INK}"><path d="M17.6 23.4H31.4V27.6C31.4 31.4 28.8 33.4 25.6 33.4H23.2C20 33.4 17.6 31.2 17.6 27.6Z"/>'
    f'<path d="M32.6 23.4H46.4V27.6C46.4 31.2 44 33.4 40.8 33.4H38.4C35.2 33.4 32.6 31.4 32.6 27.6Z"/>'
    f'<path d="M13.2 23.6H50.8V25.2H13.2Z"/></g>'
    f'<path d="M20.6 25.6L23.6 25.6M35.6 25.6L38.6 25.6" stroke="{BUTTER}" stroke-width="1.4" stroke-linecap="round"/>',
    # Murmur's own: a speech bubble with someone typing.
    "bubble": f'<path d="M45.5 1.6H58.4C61 1.6 62.6 3.2 62.6 5.8V11.6C62.6 14.2 61 15.8 58.4 15.8H50.4L45.6 19.6L46.6 15.8H45.5C42.9 15.8 41.3 14.2 41.3 11.6V5.8C41.3 3.2 42.9 1.6 45.5 1.6Z" fill="{BUTTER}" {LINE}/>'
    f'<circle cx="47.2" cy="8.7" r="1.3" fill="{INK}"/><circle cx="52" cy="8.7" r="1.3" fill="{INK}"/><circle cx="56.8" cy="8.7" r="1.3" fill="{INK}"/>',
    "headset": f'<path d="M14.2 27C14.2 15 22 8.2 32 8.2C42 8.2 49.8 15 49.8 27" stroke="{INK}" stroke-width="2.6" fill="none" stroke-linecap="round"/>'
    f'<rect x="9.6" y="22" width="7" height="12" rx="3" fill="{BLUE}" {LINE}/><rect x="47.4" y="22" width="7" height="12" rx="3" fill="{BLUE}" {LINE}/>'
    f'<path d="M12.8 33.4C13.4 38.4 17.6 40.4 23.4 39.2" stroke="{INK}" stroke-width="1.8" fill="none" stroke-linecap="round"/>'
    f'<circle cx="24.4" cy="39" r="2" fill="{BUTTER}" {LINE}/>',
    "beanie": f'<path d="M13.6 22.6C13.6 12.8 21.6 6.4 32 6.4C42.4 6.4 50.4 12.8 50.4 22.6Z" fill="{CORAL}" {LINE}/>'
    f'<rect x="12.2" y="18.4" width="39.6" height="6" rx="3" fill="{CREAM}" {LINE}/>'
    f'<path d="M17.6 18.6V24.2M22.8 18.6V24.2M28 18.6V24.2M33.2 18.6V24.2M38.4 18.6V24.2M43.6 18.6V24.2" stroke="{INK}" stroke-width=".8" opacity=".35"/>'
    f'<circle cx="32" cy="5" r="3.6" fill="{CREAM}" {LINE}/>',
    "party": f'<path d="M21.4 15.8L26.6 1.8L35.4 13.6Z" fill="{BLUE}" {LINE}/>'
    f'<path d="M23.2 11L30.2 6.6M22.4 13.4L32.6 9.8" stroke="{BUTTER}" stroke-width="1.6" stroke-linecap="round"/>'
    f'<circle cx="26.6" cy="2.4" r="2.2" fill="{BUTTER}" {LINE}/>',
    "crown": f'<path d="M21.4 14.6L20.4 4.8L25.8 9L32 2.6L38.2 9L43.6 4.8L42.6 14.6Z" fill="{BUTTER}" {LINE}/>'
    f'<circle cx="32" cy="10.8" r="1.5" fill="{CORAL}"/><circle cx="25.6" cy="12" r="1.1" fill="{BLUE}"/><circle cx="38.4" cy="12" r="1.1" fill="{BLUE}"/>',
    "halo": f'<ellipse cx="32" cy="4.6" rx="11.5" ry="3.2" fill="none" stroke="{BUTTER}" stroke-width="3.2" opacity=".45"/>'
    f'<ellipse cx="32" cy="4.6" rx="11.5" ry="3.2" fill="none" stroke="{BUTTER}" stroke-width="1.8"/>'
    f'<ellipse cx="32" cy="4.6" rx="11.5" ry="3.2" fill="none" stroke="{INK}" stroke-width=".6" opacity=".7"/>',
    "flower": petals(18.8, 14.6, 2.9, 5, ROSE, -90) + f'<circle cx="18.8" cy="14.6" r="2.3" fill="{BUTTER}" {LINE}/>'
    f'<path d="M23.4 17.2Q26.4 17.6 27.6 15.4" stroke="{MINT}" stroke-width="1.6" fill="none" stroke-linecap="round"/>',
    "sprout": f'<path d="M32 9.6V4.8" stroke="{MINT}" stroke-width="1.8" stroke-linecap="round"/>'
    f'<path d="M32 5.6C29.8 2 25.6 1.4 23.6 2.6C24.4 5.6 28.6 7.4 32 5.6Z" fill="{MINT}" {LINE}/>'
    f'<path d="M32 4.8C33.6 1.8 37.8 0.8 40.4 2C39.8 5.2 35.4 6.6 32 4.8Z" fill="{MINT}" {LINE}/>',
    "tophat": f'<g transform="rotate(-8 32 12)"><rect x="19" y="10.6" width="26" height="3.6" rx="1.8" fill="{INK}"/>'
    f'<path d="M23.6 11V2.4C23.6 1.6 24.2 1 25 1H39C39.8 1 40.4 1.6 40.4 2.4V11Z" fill="{INK}"/>'
    f'<rect x="23.6" y="7.6" width="16.8" height="2.6" fill="{CORAL}"/></g>',
    "cat": f'<path d="M15.4 20.4C14.4 15 14.8 9.6 16.4 6C20.2 7.4 23.4 10 25.4 12.6Z" fill="{INK}"/>'
    f'<path d="M48.6 20.4C49.6 15 49.2 9.6 47.6 6C43.8 7.4 40.6 10 38.6 12.6Z" fill="{INK}"/>'
    f'<path d="M17.2 16.4C16.8 13.6 17 11 17.8 9.2C19.6 10.2 21.2 11.6 22.2 13Z" fill="{ROSE}"/>'
    f'<path d="M46.8 16.4C47.2 13.6 47 11 46.2 9.2C44.4 10.2 42.8 11.6 41.8 13Z" fill="{ROSE}"/>',
    "propeller": f'<path d="M17.6 17.4C17.6 10.4 24 6.8 32 6.8C40 6.8 46.4 10.4 46.4 17.4Z" fill="{BLUE}" {LINE}/>'
    f'<path d="M32 6.8C28.4 9.6 27.6 13.6 27.8 17.4H36.2C36.4 13.6 35.6 9.6 32 6.8Z" fill="{BUTTER}"/>'
    f'<path d="M32 6.8V3.6" stroke="{INK}" stroke-width="1.3"/>'
    f'<path d="M32 3.4C28.6 1.4 24.8 1.6 23.4 3.2C25.2 4.8 29.2 4.6 32 3.4C34.8 2.2 38.8 2 40.6 3.6C39.2 5.2 35.4 5.4 32 3.4Z" fill="{CORAL}" {LINE}/>'
    f'<circle cx="32" cy="3.4" r="1.2" fill="{INK}"/>',
}


def accessory(name):
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">{ACCESSORIES[name]}</svg>\n'


def av(g, a, hue, size=48):
    layers = f'<img src="{base}/ghost-{g}.svg" alt="">' + (f'<img src="{base}/acc-{a}.svg" alt="">' if a else "")
    return f'<span class="av" style="--tile: {hue}; --s: {size}px" title="{g} + {a or "nothing"}">{layers}</span>'


def sheet():
    accs = [""] + list(ACCESSORIES)
    rows = "\n".join(
        f'<div class="row"><b>{g}</b>{"".join(av(g, a, DEVICE_HUES[i % 12]) for a in accs)}</div>'
        for i, g in enumerate(GHOSTS)
    )
    head = "".join(f"<i>{a or 'none'}</i>" for a in accs)
    picks = [("murmur", "bubble"), ("chatter", "party"), ("hum", "halo"), ("whisper", "headset"), ("coo", "flower"),
             ("rumor", "tophat"), ("echo", "specs"), ("chirp", "crown"), ("babble", "sprout"), ("psst", "shades"),
             ("jabber", "propeller"), ("natter", "cat")]
    hues = "".join(av(g, a, h) for (g, a), h in zip(picks, DEVICE_HUES))
    sizes = "".join(f'<div class="size">{"".join(av(g, a, h, n) for (g, a), h in zip(picks, DEVICE_HUES))}<i>{n}px</i></div>' for n in (36, 24, 20))
    return f"""<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Murmur ghosts</title>
<!-- Written by scripts/ghosts.py; edit the script, not this file. -->
<style>
:root {{ color-scheme: light dark; --bg: #FFFBEA; --text: #0E1B45; --quiet: #3A4670; }}
@media (prefers-color-scheme: dark) {{ :root:not([data-theme="light"]) {{ --bg: #0E1B45; --text: #FFFBEA; --quiet: #A9B6E6; }} }}
:root[data-theme="dark"] {{ --bg: #0E1B45; --text: #FFFBEA; --quiet: #A9B6E6; }}
body {{ background: var(--bg); color: var(--text); font: 13px/1.4 Inter, system-ui, sans-serif; margin: 32px 16px; }}
h1 {{ font-size: 20px; margin: 0 0 4px; }}
h2 {{ font-size: 14px; margin: 32px 0 10px; }}
p {{ color: var(--quiet); margin: 0 0 20px; max-width: 60ch; }}
.grid {{ overflow-x: auto; }}
.row, .head {{ display: grid; grid-template-columns: 64px repeat(16, 52px); align-items: center; gap: 6px; margin-bottom: 6px; width: max-content; }}
.row b {{ font-weight: 600; }}
.head i, .size i {{ font-style: normal; font-size: 11px; color: var(--quiet); text-align: center; }}
.av {{ position: relative; display: inline-block; width: var(--s); height: var(--s); }}
.av::before {{ content: ""; position: absolute; inset: 0; border-radius: 50%; background: var(--tile); }}
.av img {{ position: absolute; inset: 0; width: 100%; height: 100%; }}
.hues, .size {{ display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin-bottom: 10px; }}
.size {{ gap: 6px; }}
.size .av + .av {{ margin-left: -3px; }}
</style>
<h1>Murmur ghosts</h1>
<p>{len(GHOSTS)} ghosts &times; {len(accs)} accessories, counting none: {len(GHOSTS) * len(accs)} faces. A session's name picks one of each. The tile is the page's to paint; here each row sits on one of the twelve device colours.</p>
<div class="grid"><div class="head"><span></span>{head}</div>
{rows}</div>
<h2>A few faces, one per device colour</h2>
<div class="hues">{hues}</div>
<h2>Small</h2>
{sizes}
"""


base = "../internal/web/static/assets/ghosts"

if __name__ == "__main__":
    shutil.rmtree(OUT, ignore_errors=True)
    os.makedirs(OUT)
    for g in GHOSTS:
        with open(os.path.join(OUT, f"ghost-{g}.svg"), "w", encoding="utf-8", newline="\n") as f:
            f.write(ghost(g))
    for a in ACCESSORIES:
        with open(os.path.join(OUT, f"acc-{a}.svg"), "w", encoding="utf-8", newline="\n") as f:
            f.write(accessory(a))
    with open(SHEET, "w", encoding="utf-8", newline="\n") as f:
        f.write(sheet())
    print(f"{len(GHOSTS)} ghosts x {len(ACCESSORIES) + 1} accessories = {len(GHOSTS) * (len(ACCESSORIES) + 1)}")
