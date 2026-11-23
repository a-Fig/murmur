"""Writes Murmur's ghost avatars (#579): 16 ghosts and 15 accessories, 64x64 SVGs.

A session's avatar is three layers: a round tile the page paints, a ghost, and an accessory
or none: 16 x 16 = 256 faces. The ghosts carry no tile, so it can take the session's device
colour (#571) or any tone, and their bodies are light tints that read on all twelve device
hues. Each ghost has its own silhouette and pose, with a shared eye line and head size so the
accessories fit. The script also writes docs/ghosts.html, a sheet of every combination.
The page picks from GHOSTS and GHOST_ACCESSORIES in messages.js, which follow this order.

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

# Each ghost has its own silhouette and pose. Keep the eyes near the shared centers and
# the top of the head near y=9 so the existing hats, glasses and headset still fit.
EL, ER, EY = 25.5, 38.5, 28


def fill(d, color=INK):
    return f'<path d="{d}" fill="{color}"/>'


def line(d, color=INK, width=2):
    return f'<path d="{d}" fill="none" stroke="{color}" stroke-width="{width}" stroke-linecap="round" stroke-linejoin="round"/>'


def oval(x, y, rx, ry, color=INK):
    return f'<ellipse cx="{x}" cy="{y}" rx="{rx}" ry="{ry}" fill="{color}"/>'


def eyes(rx=2.8, ry=3.8):
    return oval(EL, EY, rx, ry) + oval(ER, EY, rx, ry)


def cheeks(color=PINK):
    return oval(19.5, 34.5, 2.8, 1.6, color) + oval(44.5, 34.5, 2.8, 1.6, color)


# The two eye shapes in the Murmur mark.
QUOTE_EYE = (
    "M129.5 49.07C127.85 48.68 124.24 50.55 123.44 53.89C122.36 58.41 122.53 61.12 126.16 61.42"
    "C129.06 61.42 129.21 60.05 129.6 57.12C129.72 56.17 130.02 55.26 130.36 54.38C131.42 51.68 131.59 49.57 129.5 49.07Z"
)
OVAL_EYE = (
    "M47.56 30.83C44.59 30.26 41.51 33.35 40.67 37.72C39.84 42.09 41.56 46.09 44.53 46.66"
    "C47.49 47.23 50.58 44.14 51.41 39.77C52.25 35.4 50.52 31.39 47.56 30.83Z"
)


def mark_eyes(path, cx, cy, scale):
    return "".join(
        f'<path d="{path}" fill="{INK}" transform="translate({x} {EY}) scale({scale}) translate({-cx} {-cy})"/>'
        for x in (EL, ER)
    )


def star(x, y, r, color=INK):
    d = "M0 -1Q.16 -.16 1 0Q.16 .16 0 1Q-.16 .16 -1 0Q-.16 -.16 0 -1Z"
    return f'<path d="{d}" fill="{color}" transform="translate({x} {y}) scale({r})"/>'


# Body, shadow, silhouette, then the details painted on top.
GHOSTS = {
    "murmur": (
        CREAM, "#DCD9EF",
        "M12 43V28C12 17 20 9 31 9C43 9 51 17 51 29V38Q51 45 56 48"
        "Q50 54 41 49Q35 57 28 50Q22 55 17 49Q12 50 12 43Z",
        mark_eyes(QUOTE_EYE, 127, 55.2, .7) + line("M29 36Q32 39 35 36")
        + line("M19 41Q20 43 23 44M43 42Q45 44 48 44", "#ABA7D1", 1.8),
    ),
    "hush": (
        "#E8E5FF", "#B6A9E5",
        "M12 35V28C12 16 20 9 32 9C44 9 52 17 52 30V40Q54 51 47 53"
        "Q42 55 35 52Q25 58 17 52Q8 53 9 45Q9 41 12 35Z",
        line("M22 28q3.5 4.5 7 0M35 28q3.5 4.5 7 0") + oval(32, 35, 1.7, 1.5)
        + fill("M10 43Q14 36 23 40Q35 47 51 41Q54 51 47 53Q42 55 35 52Q25 58 17 52Q9 53 10 43Z", "#AC9BDA")
        + line("M11 43Q14 36 23 40Q35 47 51 41", "#645191", 1.8)
        + star(23, 46, 2.5, CREAM) + star(36, 49, 2.1, CREAM),
    ),
    "whisper": (
        "#DEF0FF", "#A9C7F0",
        "M13 38V29C13 17 21 9 32 9C43 9 51 17 51 29Q50 40 55 43"
        "Q59 45 57 48Q50 53 40 49Q30 56 20 50Q13 47 13 38Z",
        oval(27, 28, 2.4, 3.5) + oval(40, 28, 2.4, 3.5)
        + line("M23 23l5-1M37 22l5 1", INK, 1.5)
        + line("M31 36h7")
        + fill("M43 46Q36 44 35 39l-.5-7q.1-3 2.7-2.8q2 .1 2.4 2.7l.9 5q3.6-.5 5 2.4q1.6 3.6-2.5 6.7Z", "#F6FAFF")
        + line("M43 46Q36 44 35 39l-.5-7q.1-3 2.7-2.8q2 .1 2.4 2.7l.9 5q3.6-.5 5 2.4", "#506796", 1.7),
    ),
    "mumble": (
        "#FFF3C6", "#E7CA82",
        "M13 28C13 17 20 9 31 9C43 9 50 16 51 28L54 47Q54 54 46 53"
        "Q42 57 36 53Q31 57 25 53Q19 56 15 52Q8 52 10 46Z",
        mark_eyes(OVAL_EYE, 46, 38.7, .52) + cheeks()
        + fill("M11 37Q31 42 53 37L54 43Q46 48 29 46l-5 9-4-4-5 2 3-9-8-2Z", "#E97D75")
        + line("M11 37Q31 42 53 37L54 43Q46 48 29 46l-5 9-4-4-5 2 3-9-8-2Z", "#814551", 1.5)
        + line("M19 44l9 2M44 40q-1 2-.5 4", "#FFD4BA", 1.8),
    ),
    "chatter": (
        "#FFE1C6", "#F3AA95",
        "M14 28C14 17 21 9 32 9C43 9 50 17 50 28Q52 26 54 20Q56 16 59 19"
        "Q63 25 56 34l-4 4Q55 46 49 51Q44 55 38 50Q32 57 25 51Q16 54 13 46l-2-8"
        "Q3 34 4 26Q4 21 7 22Q10 22 10 26Q11 28 14 28Z",
        line("M22 28q3.5-6 7 0m6 0q3.5-6 7 0") + cheeks("#F28D87")
        + fill("M25 33Q32 35 39 33Q40 42 32 42Q25 42 25 33Z")
        + fill("M27.5 40Q32 36 36.5 40Q32 44 27.5 40Z", PINK),
    ),
    "babble": (
        "#D9F8E6", "#92D1B7",
        "M14 27C14 16 22 9 32 9C43 9 51 17 51 28Q51 34 55 39Q60 48 50 52"
        "Q43 57 37 51Q32 58 25 53Q15 58 12 49Q4 46 9 39Q13 34 14 27Z",
        eyes(3.3, 4.2) + oval(26.5, 26.5, 1, 1.1, CREAM) + oval(39.5, 26.5, 1, 1.1, CREAM)
        + line("M27 34.5Q32 39 37 34.5") + fill("M31 36h5v2.5q-2 5-5 1Z", "#EC829E")
        + oval(21, 44, 3.5, 3.5, "#74BCAC") + oval(26, 49, 2.1, 2.1, "#74BCAC")
        + oval(47, 43, 5.2, 5.2, "#74BCAC") + line("M44.5 42q.5-2.5 3-2.5", "#F0FFF6", 1.4),
    ),
    "rumor": (
        "#F1DDF9", "#C5A1DE",
        "M13 28C13 17 20 9 31 9C43 9 51 17 51 28l1 9Q54 44 57 45"
        "Q53 53 45 51Q39 57 30 53Q17 57 11 50Q7 46 10 38Z",
        fill("M21 27h9q-1 6-4 6t-5-6Zm13 0h9q-1 6-4 6t-5-6Z")
        + line("M29 37q4 2 7-2")
        + fill("M10 38Q19 40 29 46Q40 44 52 37Q54 44 57 45Q53 53 45 51Q39 57 30 53Q17 57 11 50Q7 46 10 38Z", "#564177")
        + line("M10 38Q19 40 29 46Q40 44 52 37", "#9474B4", 2)
        + oval(29, 47, 2.5, 2.5, BUTTER),
    ),
    "echo": (
        "#DCE9FF", "#9CB9EF",
        "M13 29C13 17 21 9 32 9C43 9 50 17 50 28V35Q52 43 58 40"
        "Q59 47 50 49Q48 55 40 53Q34 58 27 53Q18 56 12 48Q8 42 13 29Z",
        eyes(3.4, 4.2) + star(26.4, 26.8, 2.2, CREAM) + star(39.4, 26.8, 2.2, CREAM)
        + line("M29 35q3 4 6 0")
        + line("M16 41q7 9 16 4q7 5 13 1M19 47q5 5 12 3q5 4 11 1", "#5D83CD", 2),
    ),
    "chirp": (
        "#FFF0A0", "#E9BC58",
        "M14 29C14 17 21 9 32 9C43 9 50 17 50 28Q54 27 58 23"
        "Q61 24 58 32Q56 37 51 39Q55 48 49 50l-6-1q-4 8-10 3q-6 6-12-1Q12 52 12 45l-1-7Q5 37 6 29q1-3 4-1Z",
        line("M22 29q3.5-5 7 0m6 0q3.5-5 7 0") + cheeks("#F5B38F")
        + fill("M27 34q5-3 10 0l-5 6Z", "#A95A3E")
        + line("M13 38l5 4m33-3-5 5M26 47q3 2 6-.5q3 2.5 6 .5", "#BA752E", 1.8),
    ),
    "coo": (
        "#FFE1E9", "#E7A0B9",
        "M13 29C13 17 20 9 32 9C43 9 51 17 51 29l1 7q7 5 4 11q-3 5-10 4"
        "q-6 7-14 2q-7 5-14-1q-9 0-11-6q-2-5 5-10Z",
        eyes(2.5, 3.2) + cheeks() + line("M29 35q3 3 6 0")
        + fill("M32 44C25 36 18 44 24 50l8 6l8-6c6-6-1-14-8-6Z", "#CB416A")
        + line("M24 46q-2-3 2-4", "#FFDDE9", 1.5)
        + line("M14 42q1 5 8 6m28-6q-1 5-8 6", "#A45D7A", 2),
    ),
    "hum": (
        "#E5F6C9", "#ACCC8D",
        "M13 28C13 17 21 9 32 9C44 9 52 17 52 29v8Q53 45 58 48"
        "Q51 55 42 51Q39 58 30 53Q23 56 19 51Q9 52 9 44Z",
        line("M22 26q3.5 6 7 0m6 0q3.5 6 7 0") + line("M29.5 34.5q2.5 3.5 5 0")
        + fill("M26 48v-7l13-3v9q-.4 2.7-4 2.7q-3 0-2.6-2q.2-2.3 4.2-2.4v-3.4l-8.3 1.9v6q-.7 2.7-4.1 2.7q-2.9 0-2.5-2q.3-2.3 4.3-2.5Z", "#49795D"),
    ),
    "psst": (
        "#DDF5F1", "#90CFC8",
        "M13 28C13 17 21 9 32 9C43 9 51 17 51 29l2 8Q59 45 53 50"
        "Q48 56 37 50Q31 57 23 52Q12 55 6 46Q13 43 13 36Z",
        oval(EL, EY, 3, 4) + line("M35 28q3.5-5 7 0") + line("M28 35q3 5 8 1")
        + fill("M37 40l12-2 1.5 9-12 2Z", CREAM)
        + line("M37 40l12-2 1.5 9-12 2Z M38 41l6 3 4-4", "#507B84", 1.5)
        + line("M17 39q3 5 9 6", "#6DAAA4", 2),
    ),
    "sigh": (
        "#E5F2FF", "#A5C7E9",
        "M13 29C13 17 21 9 31 9C43 9 51 17 51 28Q51 38 56 42"
        "Q57 49 48 49Q46 55 38 51Q35 59 26 55Q16 59 12 51Q4 49 7 43Q12 38 13 29Z",
        line("M21 27q4 5 8 1m6 0q4 4 8-1") + oval(33, 36, 2.8, 3.4)
        + line("M35 43q7-4 13 0q3 2 6 0m-24 6q6-3 11 0", "#628DC1", 2)
        + oval(56, 35, 3, 2, "#D0E8FC"),
    ),
    "jabber": (
        "#FCE3F1", "#E7A5CD",
        "M13 29C13 17 21 9 32 9C43 9 51 17 51 28L59 34L53 39"
        "Q56 50 47 53Q41 57 34 51Q29 57 23 52Q13 54 11 45L5 38L12 35Z",
        star(EL, EY, 5) + star(ER, EY, 5)
        + fill("M25 36Q32 32 39 36Q38 45 32 45Q26 45 25 36Z")
        + fill("M27.5 42Q32 38 36.5 42Q32 47 27.5 42Z", PINK)
        + line("M13 39l6 4m32-4-6 4", "#AD568C", 2),
    ),
    "natter": (
        "#E9F7DE", "#B1D4AA",
        "M13 28C13 17 21 9 32 9C43 9 51 17 51 28Q54 36 55 43"
        "Q57 53 48 53Q43 53 39 50Q31 58 24 53Q17 57 11 51Q6 48 9 40Z",
        eyes(2.8, 3.5) + line("M27 35q2 4 5 2q3 2 5-2")
        + oval(18, 33, 1, 1, "#A47166") + oval(21, 34.5, 1, 1, "#A47166")
        + oval(46, 33, 1, 1, "#A47166") + oval(43, 34.5, 1, 1, "#A47166")
        + line("M15 39q1 7 12 8q3-5 6-3m16-5q-1 7-12 8q-3-5-6-3", "#57866F", 2),
    ),
    "buzz": (
        "#FFEDA0", "#EABB58",
        "M13 29C13 17 21 9 32 9C44 9 51 17 51 28Q57 25 59 29"
        "Q61 36 53 38l3 9-9-1-4 8-8-4-7 6-6-6-9 3-2-10Q3 41 5 35Q7 30 13 31Z",
        mark_eyes(QUOTE_EYE, 127, 55.2, .7) + line("M30 34l2 3 4-2")
        + line("M15 43q18 3 34-4m-28 10q14 0 26-5", "#98603C", 2.8),
    ),
}


def ghost(name):
    body, shade, d, details = GHOSTS[name]
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">\n'
        f'  <defs><clipPath id="body"><path d="{d}"/></clipPath></defs>\n'
        f'  {oval(32, 58, 14, 2, "#00000026")}\n'
        f'  <g clip-path="url(#body)">{fill(d, shade)}'
        f'<path d="{d}" fill="{body}" transform="translate(-1.5 -3.5)"/></g>\n'
        f'  <path d="{d}" fill="none" stroke="{INK}" stroke-width="1.65" stroke-linejoin="round"/>\n'
        f'  {details}\n'
        '</svg>\n'
    )


# The page's twelve device hues (#571, style.css), which the sheet uses for tiles.
DEVICE_HUES = [
    "oklch(0.57 0.085 15)", "oklch(0.62 0.09 45)", "oklch(0.66 0.09 82)", "oklch(0.6 0.08 115)",
    "oklch(0.57 0.075 145)", "oklch(0.62 0.075 180)", "oklch(0.57 0.07 210)", "oklch(0.6 0.075 238)",
    "oklch(0.56 0.09 290)", "oklch(0.6 0.09 318)", "oklch(0.55 0.09 348)", "oklch(0.5 0.05 60)",
]


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
