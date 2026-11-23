"""Writes Murmur's ghost avatars (#579): 16 ghosts and 16 accessories, 64x64 SVGs.

A session's avatar is three layers: a round tile the page paints, a ghost, and an accessory.
There are 16 x 16 = 256 faces. The ghosts carry no tile, so it can take the session's device
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


# Body, shadow, silhouette, then the face and folds. Anything worn lives in ACCESSORIES.
GHOSTS = {
    "murmur": (
        CREAM, "#DCD9EF",
        "M11 43V29C11 17 20 8 32 8C44 8 52 17 52 29V37Q52 44 58 48"
        "Q51 55 40 48Q34 58 27 50Q21 55 16 50Q11 50 11 43Z",
        mark_eyes(QUOTE_EYE, 127, 55.2, .7) + line("M29 37Q32 40 35 37")
        + line("M18 40q0 5 5 6M44 41q2 4 6 4", "#A59BC4", 1.8),
    ),
    "hush": (
        "#E8E5FF", "#B6A9E5",
        "M9 34Q13 32 13 28C12 19 17 11 25 8C36 4 49 12 51 24"
        "Q53 31 50 37Q48 42 54 44Q60 46 59 49Q56 55 44 51Q35 57 27 52"
        "Q15 57 7 51Q2 48 5 43Q8 39 9 34Z",
        line("M21 29q4 4.5 8 0M35 28q4 4.5 8 0", INK, 2.3)
        + oval(31, 38, 4.5, 5.2) + oval(31, 41, 2.5, 1.7, "#D99EB5")
        + line("M11 46q5 4 11 0m17 2q6-3 7-8", "#9180C9", 2),
    ),
    "whisper": (
        "#DFF4FE", "#9BCBEE",
        "M13 42Q10 36 12 29L14 24C16 14 22 9 31 9C41 9 50 15 50 24"
        "Q48 27 52 30Q56 32 55 35Q54 37 50 38Q51 43 57 46"
        "Q52 53 44 49Q37 55 29 50Q19 55 13 42Z",
        oval(28, 28, 2.1, 2.9) + oval(41, 28, 2.4, 4.2)
        + line("M23 25l6-.7m8-3q3.5-3.6 7-.6", INK, 1.8)
        + line("M39 37l7 1")
        + line("M23 43q2-6 10-7", "#638ABC", 2),
    ),
    "mumble": (
        "#FFF1B6", "#E3BD6B",
        "M10 25C10 15 19 10 30 10Q36 7 42 12Q54 15 54 27"
        "L55 42Q57 50 50 51Q47 56 40 52Q36 57 30 52Q24 56 20 51Q11 55 9 47Z",
        mark_eyes(OVAL_EYE, 46, 38.7, .47)
        + line("M22 24l5-2.6m10 0l5 2.6", INK, 1.7)
        + oval(19, 35, 3.6, 2.6, "#E8A981") + oval(45, 35, 3.6, 2.6, "#E8A981")
        + line("M26 39q2-4 4-1q2-3 4-.5q2-3 4 .5")
        + line("M21 46q3 4 6 2m13 0q4-2 5-4", "#B98542", 1.8),
    ),
    "chatter": (
        "#FFE1C6", "#EEAA94",
        "M14 29C14 19 18 11 28 9C41 6 50 15 50 28Q54 28 56 22"
        "Q58 18 61 21Q64 27 57 33L52 37Q55 46 48 51Q44 55 37 50"
        "Q32 58 24 52Q14 56 11 46l-1-8Q2 34 3 27Q4 22 7 23Q9 24 10 28Z",
        line("M21 28q4.5-7 9 0m4 0q4.5-7 9 0", INK, 2.4) + cheeks("#F4968A")
        + fill("M24 34Q32 36 40 34Q41 44 32 45Q23 44 24 34Z")
        + fill("M27 42Q32 38 37 42Q32 47 27 42Z", PINK),
    ),
    "babble": (
        "#D9F8E8", "#80C8AB",
        "M13 29Q9 22 15 16Q21 10 28 10Q35 5 43 10Q55 14 52 28"
        "Q50 33 55 41Q58 47 53 51Q47 56 39 52Q35 57 30 53Q24 56 19 52"
        "Q8 54 7 47Q6 41 11 36Q14 33 13 29Z",
        oval(24, 28, 4.5, 5.8, "#F7FFF4") + oval(40, 27, 4.5, 5.8, "#F7FFF4")
        + oval(26.2, 29, 2.5, 3.3) + oval(37.8, 28, 2.5, 3.3)
        + line("M26 36Q32 40 38 34.5") + fill("M31 37.5l7-1v3q-2.5 7-7 2Z", "#EC829E")
        + line("M17 21q0-5 6-6m-9 30q3 4 10 2m21 1q4-1 5-4", "#83C8AE", 2),
    ),
    "rumor": (
        "#D6B9F8", "#A27AD6",
        "M11 33Q10 25 14 18C18 10 26 9 33 8C46 7 53 16 52 27"
        "Q50 36 55 40L60 43Q54 48 49 46Q47 53 40 51Q32 57 25 50"
        "Q17 55 10 49Q5 44 11 33Z",
        fill("M20 26l10 2q-1 5-5 5q-4-1-5-7Zm14 2l10-3q0 6-4 7q-4 1-6-4Z")
        + line("M35 22q4-3 8-1m-15 17q6 3 12-3l-2-.4", INK, 2.1)
        + line("M12 41q7-2 13 4m18 1q6-2 7-7", "#906AB8", 2),
    ),
    "echo": (
        "#ADCFFF", "#77A2E8",
        "M13 32Q11 18 22 12Q28 7 35 10Q48 11 51 24Q52 31 50 34"
        "Q51 42 60 38Q63 44 55 49Q51 51 46 49Q43 55 36 53Q29 58 24 52"
        "Q15 56 10 48Q6 42 13 32Z",
        eyes(4.1, 5) + star(26.4, 26.2, 2.4, CREAM) + star(39.4, 26.2, 2.4, CREAM)
        + oval(32, 38.8, 3, 3.9)
        + line("M12 42q7 10 17 4q8 4 13 0q6 2 13-3M17 49q5 4 12 1q6 3 11 0", "#4A75B9", 1.9),
    ),
    "chirp": (
        "#FFF19A", "#E7B449",
        "M14 30C13 21 17 15 23 12Q28 7 34 9C44 9 51 16 51 27"
        "Q56 26 59 20Q62 25 58 34Q55 37 51 38Q53 45 47 48Q45 53 39 50"
        "Q34 57 28 52Q24 55 19 50Q12 52 11 44Q3 42 5 33Q7 31 14 30Z",
        line("M20 29l5-5 4 4m6 0 4-4 5 5", INK, 2.2) + cheeks("#F5BB79")
        + fill("M27 34q5-3 10 0q-2 5-5 5q-3 0-5-5Z", "#9A5037")
        + line("M11 39q4 2 7 1m36-7q-2 5-7 7m-20 7q3 0 4-2q2 2 5 2", "#BA7639", 1.8),
    ),
    "coo": (
        "#FFE0ED", "#E898B8",
        "M11 33C9 24 12 15 20 11Q27 7 32 12Q38 6 45 11Q55 16 53 29"
        "Q53 35 56 42Q57 51 48 52Q42 56 34 52Q27 58 20 52Q11 55 7 48"
        "Q3 42 11 33Z",
        fill("M25.5 25C22 21 17 27 22 31l3.5 3 3.5-3c5-4 0-10-3.5-6ZM38.5 25C35 21 30 27 35 31l3.5 3 3.5-3c5-4 0-10-3.5-6Z", "#A13864")
        + oval(19.5, 35, 3.9, 2.3, "#ED91AE") + oval(44.5, 35, 3.9, 2.3, "#ED91AE")
        + line("M28.5 37q3.5 4 7 0")
        + line("M11 43q5 5 12 3m30-3q-5 5-12 3", "#BA6E96", 1.9),
    ),
    "hum": (
        "#EBF4BD", "#ACCA7F",
        "M14 31Q12 23 17 17Q21 10 31 9C42 6 52 14 52 25"
        "Q52 34 45 40Q42 44 49 46Q53 47 58 44Q58 52 48 54"
        "Q40 56 34 52Q23 58 19 49Q5 46 12 36Z",
        line("M21 28q4 4 8 0m6 0q4 4 8 0", INK, 2.1)
        + line("M26 36q5 5 11 0", INK, 2.1) + oval(20, 34, 3.3, 1.7, "#CCDA78")
        + oval(44, 34, 3.3, 1.7, "#CCDA78")
        + line("M18 41q6 6 17 3", "#7E9E5D", 2),
    ),
    "psst": (
        "#D4F3ED", "#80C9BE",
        "M13 28Q11 21 16 15Q22 8 32 9C42 9 50 16 51 26"
        "Q53 34 50 39Q55 45 51 49Q45 56 35 51Q29 58 22 52"
        "Q13 53 6 46Q12 45 12 38Z",
        oval(EL, EY, 3, 4.2) + line("M35 28q3.5-6 7 0", INK, 2.2)
        + line("M28 36q3 4 8 1")
        + fill("M45 49Q35 47 35 42l-1-7q0-3 2-3q3-.6 3 2l2 6q3-2 6 .5q3 3-2 8.5Z", "#B9E6DF")
        + line("M44 49Q35 47 35 42l-1-7q0-3 2-3q3-.6 3 2l2 6q3-2 6 .5", "#4B8B8B", 1.75),
    ),
    "sigh": (
        "#E4F1FF", "#A1BDDC",
        "M12 29Q10 20 17 14Q23 9 32 10Q42 7 47 15Q52 19 52 28"
        "Q50 33 54 40Q59 46 55 52Q51 57 44 52Q43 58 35 54"
        "Q29 60 23 55Q14 57 10 51Q5 45 9 38Z",
        line("M21 26q4 5 8 1m6 0q4 4 8-1", INK, 2.2)
        + fill("M21 30q2 1 5 0v9q0 3-2 3q-3 0-3-3Zm17 0q3 1 5-1v13q0 3-2 3q-3 0-3-3Z", "#719FD8")
        + oval(32, 39, 2.6, 3.3),
    ),
    "jabber": (
        "#FCE0F1", "#E395C7",
        "M13 29C12 20 17 11 25 9Q31 5 36 9C46 10 52 17 51 29"
        "L60 33L53 39Q52 54 45 50L39 55L32 50L25 55L18 49Q12 50 10 42L4 36L12 34Z",
        star(EL, EY, 5.4) + star(ER, EY, 5.4)
        + fill("M24 41Q25 34 32 34Q39 34 40 41Q39 47 32 47Q25 47 24 41Z")
        + fill("M27 44Q31 40 37 44Q32 49 27 44Z", PINK)
        + line("M12 38l5 3m35-4-5 4", "#A65A93", 2),
    ),
    "natter": (
        "#E0F3D6", "#A0C69A",
        "M10 26Q9 16 16 12Q21 9 29 10L41 9Q53 9 54 22L54 33"
        "Q56 38 57 44Q57 53 47 53Q42 53 39 50Q36 55 30 55Q24 55 21 51"
        "Q17 54 12 53Q5 52 6 44Q7 36 10 26Z",
        fill("M20 27h10q-1 5-5 5t-5-5Zm14 0h10q-1 5-5 5t-5-5Z")
        + line("M27 38h10", INK, 2.2)
        + oval(17.5, 33, 1.15, 1.15, "#8D745C") + oval(21, 35, 1.15, 1.15, "#8D745C")
        + oval(46.5, 33, 1.15, 1.15, "#8D745C") + oval(43, 35, 1.15, 1.15, "#8D745C")
        + line("M9 43q5 5 13 2m20 0q8 3 13-2", "#648E6D", 1.8),
    ),
    "buzz": (
        "#FFD887", "#E89A51",
        "M12 28Q9 21 15 15Q19 9 27 9L34 5L37 10Q46 10 50 19"
        "Q52 24 50 29L60 26L55 37L58 44L48 44L44 53L36 49L30 56"
        "L24 49L14 53L12 45L5 43L11 35Z",
        oval(24.5, 26, 3.6, 5) + oval(40, 29, 2.2, 3.2)
        + line("M20 20l5-2m13 3 5 2M26 38l3-3 3 3 3-2 3 3", INK, 2.1)
        + line("M13 40q7 0 11 4m16 0q7-1 10-6", "#AC633D", 2),
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
    "moon": f'<path d="M45 4.6C41.1 9.2 45.4 16.2 52 13.3C50.8 18.4 45.2 20.5 41 17.9C35.2 14.2 37.4 5.8 45 4.6Z" fill="{BUTTER}" {LINE}/>'
    + star(51, 7, 3.5) + star(51, 7, 2.2, "#D9EDFF"),
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
    layers = (f'<img src="{base}/ghost-{g}.svg" alt="">' if g else "") + (f'<img src="{base}/acc-{a}.svg" alt="">' if a else "")
    label = " + ".join(filter(None, [g, accessory_label(a) if a else ""]))
    cls = "av" if g else "av accessory"
    return f'<span class="{cls}" style="--tile: {hue}; --s: {size}px" title="{label}">{layers}</span>'


def accessory_label(name):
    return {"moon": "moon clip", "specs": "round specs", "party": "party hat", "tophat": "top hat", "cat": "cat ears"}.get(name, name)


def specimen(g, a, hue, caption=""):
    name = g or accessory_label(a)
    caption = accessory_label(a) if g and a else caption
    detail = f'<small>{caption}</small>' if caption else ""
    return f'<figure>{av(g, a, hue, 64)}<figcaption>{name}{detail}</figcaption></figure>'


def sheet():
    accs = list(ACCESSORIES)
    rows = "\n".join(
        f'<div class="row"><b>{g}</b>{"".join(av(g, a, DEVICE_HUES[i % 12]) for a in accs)}</div>'
        for i, g in enumerate(GHOSTS)
    )
    head = "".join(f"<i>{accessory_label(a)}</i>" for a in accs)
    picks = [("murmur", "bubble"), ("chatter", "party"), ("hum", "halo"), ("whisper", "headset"), ("coo", "flower"),
             ("rumor", "tophat"), ("echo", "specs"), ("chirp", "crown"), ("babble", "sprout"), ("psst", "shades"),
             ("jabber", "propeller"), ("natter", "cat")]
    moods = {
        "murmur": "gentle", "hush": "sleepy", "whisper": "skeptical", "mumble": "nervous",
        "chatter": "giddy", "babble": "goofy", "rumor": "smug", "echo": "awestruck",
        "chirp": "eager", "coo": "smitten", "hum": "dreamy", "psst": "sneaky",
        "sigh": "heartbroken", "jabber": "excitable", "natter": "deadpan", "buzz": "frazzled",
    }
    bodies = "".join(specimen(g, "", DEVICE_HUES[i % 12], moods[g]) for i, g in enumerate(GHOSTS))
    gear = "".join(specimen("", a, "var(--swatch)") for a in accs)
    examples = [("hush", "moon"), ("whisper", "headset"), ("chatter", "party"), ("rumor", "tophat"),
                ("echo", "specs"), ("coo", "flower"), ("psst", "shades"), ("jabber", "propeller")]
    pairings = "".join(specimen(g, a, DEVICE_HUES[i]) for i, (g, a) in enumerate(examples))
    sizes = "".join(f'<div class="size">{"".join(av(g, a, h, n) for (g, a), h in zip(picks, DEVICE_HUES))}<i>{n}px</i></div>' for n in (36, 24, 20))
    return f"""<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Murmur ghosts</title>
<!-- Written by scripts/ghosts.py; edit the script, not this file. -->
<style>
:root {{ color-scheme: light dark; --bg: #FFFBEA; --text: #0E1B45; --quiet: #3A4670; --swatch: #D8DDF0; }}
@media (prefers-color-scheme: dark) {{ :root:not([data-theme="light"]) {{ --bg: #0E1B45; --text: #FFFBEA; --quiet: #A9B6E6; --swatch: #54638D; }} }}
:root[data-theme="dark"] {{ --bg: #0E1B45; --text: #FFFBEA; --quiet: #A9B6E6; --swatch: #54638D; }}
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
.av.accessory {{ height: 50px; }}
.av.accessory::before {{ border-radius: 12px; }}
.av.accessory img {{ top: 9px; height: var(--s); }}
.specimens {{ display: grid; grid-template-columns: repeat(8, minmax(0, 1fr)); gap: 14px 4px; max-width: 816px; }}
.specimens figure {{ margin: 0; text-align: center; }}
.specimens figcaption {{ margin-top: 4px; font-weight: 600; }}
.specimens small {{ display: block; color: var(--quiet); font-size: 11px; font-weight: 400; }}
@media (max-width: 650px) {{ .specimens {{ grid-template-columns: repeat(4, minmax(0, 1fr)); }} }}
.size {{ display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin-bottom: 10px; }}
.size {{ gap: 6px; }}
.size .av + .av {{ margin-left: -3px; }}
</style>
<div class="showcase">
<h1>Murmur ghosts</h1>
<p>{len(GHOSTS)} ghosts &times; {len(accs)} accessories = {len(GHOSTS) * len(accs)} pairings. Each session wears one of each.</p>
<h2>Base ghosts</h2>
<div class="specimens">{bodies}</div>
<h2>Accessories</h2>
<div class="specimens">{gear}</div>
<h2>Example pairings</h2>
<div class="specimens">{pairings}</div>
</div>
<h2>All pairings</h2>
<div class="grid"><div class="head"><span></span>{head}</div>
{rows}</div>
<h2>In-app sizes</h2>
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
    print(f"{len(GHOSTS)} ghosts x {len(ACCESSORIES)} accessories = {len(GHOSTS) * len(ACCESSORIES)}")
