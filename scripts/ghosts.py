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

# Hats and eyewear rely on the crown and eyes staying near these shared positions.
EL, ER, EY = 25.5, 38.5, 28


def fill(d, color=INK):
    return f'<path d="{d}" fill="{color}"/>'


def line(d, color=INK, width=2):
    return f'<path d="{d}" fill="none" stroke="{color}" stroke-width="{width}" stroke-linecap="round" stroke-linejoin="round"/>'


def oval(x, y, rx, ry, color=INK):
    return f'<ellipse cx="{x}" cy="{y}" rx="{rx}" ry="{ry}" fill="{color}"/>'


def eyes(rx=2.8, ry=3.8):
    return oval(EL, EY, rx, ry) + oval(ER, EY, rx, ry)


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


# Flat silhouettes and a few large features stay legible in a conversation row.
# Each contour and its folds are drawn together. Wearable shapes are in ACCESSORIES.
GHOSTS = {
    "murmur": (
        CREAM,
        "M8 43C9 34 15 31 16 24C18 14 25 7 34 8C45 8 51 17 51 27"
        "C51 37 51 44 57 48C50 53 44 49 40 46C36 55 29 54 25 47C20 54 11 52 8 43Z",
        fill("M40 46C42 38 46 36 51 32C51 39 52 44 57 48C50 53 44 49 40 46Z", "#CCDAFA")
        + mark_eyes(QUOTE_EYE, 127, 55.2, .72)
        + line("M28 37Q31 41 35 37", INK, 2.6),
    ),
    "hush": (
        "#DED7FF",
        "M6 38C7 31 13 30 14 22C17 13 24 9 33 10C44 10 49 17 49 25"
        "C49 38 57 37 59 43C61 50 48 54 40 49C35 53 27 52 23 49C16 55 5 51 6 38Z",
        fill("M7 42C12 48 17 40 23 45C28 49 33 50 40 45C48 50 54 48 59 44"
             "C60 50 48 54 40 49C35 53 27 52 23 49C16 55 7 51 7 42Z", "#AE9DE7")
        + line("M19 29q4 5 9 1m8 0q4 4 8 0", "#3F306D", 2.6)
        + oval(32, 39, 4.5, 5, "#3F306D")
        + oval(32, 41, 2.6, 1.5, "#D389AD"),
    ),
    "whisper": (
        "#B5DFFF",
        "M18 19C23 10 34 6 42 11C48 15 48 20 48 23C54 25 58 28 57 32"
        "C57 36 50 35 49 39C49 44 54 46 57 46C52 54 45 54 40 50"
        "C32 56 19 51 14 44C8 36 12 27 18 19Z",
        fill("M14 44C8 36 12 27 18 19C19 25 15 30 21 34Q27 40 21 50Q17 48 14 44Z", "#6DB5E8")
        + oval(30, 27.5, 2.6, 3.6) + oval(43, 28, 2.2, 3.5)
        + line("M25 23l6-2m9 .5q3-2 6 0M44 37l5-.5", INK, 2.1),
    ),
    "mumble": (
        "#FCE79B",
        "M10 27C9 17 16 11 22 11Q29 6 35 10C44 7 53 15 53 24L55 38"
        "C58 48 52 53 46 51Q41 56 34 51Q28 55 22 51C14 56 8 51 9 44Z",
        fill("M10 39C15 40 16 45 20 44C17 51 12 51 9 48ZM54 36C49 39 48 44 46 48"
             "C51 48 54 46 55 43Z", "#E9B76A")
        + mark_eyes(OVAL_EYE, 46, 38.7, .56)
        + line("M20 23q4 0 7-3m10 0q2 3 7 3M27 39l3-2 3 2 3-2 3 2", "#453342", 2)
        + oval(17.5, 34, 3.7, 2.6, "#E89E7C") + oval(45.5, 34, 3.7, 2.6, "#E89E7C"),
    ),
    "chatter": (
        "#FFBF95",
        "M13 27C13 15 21 9 32 9C43 9 51 16 51 27C57 24 56 17 60 18"
        "C64 19 62 26 58 32L52 36C55 45 49 50 43 50C39 57 32 53 30 48"
        "C26 56 16 53 13 46L11 38C5 36 1 31 2 25C3 20 8 22 8 26Q9 28 13 27Z",
        fill("M12 38C14 49 26 49 29 43Q36 51 43 44Q49 45 52 40C52 46 48 50 43 50"
             "C39 57 32 53 30 48C26 56 16 53 13 46Z", "#EF8A75")
        + line("M19 28q5.5-8 11 0m4 0q5.5-8 11 0", "#47254C", 3)
        + fill("M21 34Q32 37 43 34C42 44 37 48 32 48C26 48 22 43 21 34Z", "#47254C")
        + fill("M26 45q6-7 12 0q-6 5-12 0Z", "#F986A2"),
    ),
    "babble": (
        "#A6ECCB",
        "M8 41C8 32 14 30 12 23C10 14 17 10 24 12C28 5 39 6 43 12"
        "C51 13 56 20 53 28C52 34 57 37 57 44C57 51 50 55 43 50"
        "C38 55 31 55 26 51C17 56 8 51 8 41Z",
        fill("M8 42C11 48 21 49 27 46C32 52 40 48 43 45Q50 51 57 44"
             "C57 51 50 55 43 50C38 55 31 55 26 51C17 56 8 51 8 42Z", "#5BC5A2")
        + oval(23, 26.5, 5.4, 6.3, "#F0FFEF") + oval(40, 28.5, 5.4, 6.3, "#F0FFEF")
        + oval(25.2, 28, 2.8, 3.5) + oval(38, 29.5, 2.8, 3.6)
        + fill("M27 37q5 3 12-1q-.5 8-7 8q-4 0-5-7Z")
        + fill("M34 40q4-2 5 0q2 6-2 7q-3 0-3-3Z", "#E97098"),
    ),
    "rumor": (
        "#C7AFFA",
        "M9 45C17 38 12 29 14 23C16 15 24 9 34 9C46 7 54 14 52 26"
        "C51 34 53 41 60 45C55 48 47 48 42 45C40 54 31 56 25 48C19 51 12 50 9 45Z",
        fill("M34 9C46 7 54 14 52 26C51 34 53 41 60 45C55 48 47 48 42 45"
             "C45 37 43 36 46 28C47 19 42 15 34 9Z", "#9472CF")
        + fill("M20 26l11 2q-2 5-6 4q-4 0-5-6Zm15 2l11-3q0 5-5 7q-4 1-6-4Z", "#382151")
        + line("M37 21q4-4 8-2m-17 19q7 5 14-2", "#382151", 2.5),
    ),
    "echo": (
        "#A4D6FF",
        "M12 30C11 18 18 11 28 9C41 6 51 14 51 26C51 33 54 36 61 34"
        "Q59 44 50 44Q49 52 39 50Q35 57 27 51C17 57 6 50 8 41Q8 35 12 30Z",
        fill("M9 39C12 46 20 47 26 43Q32 47 39 42Q47 45 52 38Q58 38 61 34"
             "Q59 44 50 44Q49 52 39 50Q35 57 27 51C17 57 6 50 8 41Z", "#78A5ED")
        + eyes(4.6, 5.6) + star(25.1, 26.2, 2.8, CREAM) + star(38.2, 26.2, 2.8, CREAM)
        + oval(32, 39, 3.8, 4.4),
    ),
    "chirp": (
        "#FFEC85",
        "M14 31C13 20 18 13 26 10Q32 6 37 10C46 11 51 17 50 27"
        "Q54 26 59 18C62 28 59 34 52 37Q54 42 50 46Q44 54 38 49"
        "Q32 57 25 50Q18 54 15 48Q5 48 4 36Q8 33 14 31Z",
        fill("M4 36Q13 37 21 35Q17 40 12 42Q8 42 6 41ZM49 36Q57 28 59 18"
             "C62 28 59 34 52 37Q52 40 51 42Q47 40 49 36Z", "#EDA94E")
        + line("M20 29l5-6 5 5m5 0 5-5 5 6", "#573737", 2.55)
        + fill("M26 35q6-5 12 0q-3 7-6 7t-6-7Z", "#BE6145"),
    ),
    "coo": (
        "#FFC6DC",
        "M9 25C8 16 15 10 23 10Q29 10 32 15Q39 5 48 11C57 16 56 24 53 31"
        "Q56 36 54 41C50 49 40 51 32 57C27 52 15 50 10 42Q6 36 9 25Z",
        fill("M10 38Q15 46 23 45Q31 47 32 57C27 52 15 50 10 42ZM40 48Q46 41 54 38Q51 47 40 50Z", "#EF92B5")
        + fill("M24 25C21 20 16 26 20 30l4 4 4-4c4-4-1-10-4-5ZM39 25C36 20 31 26 35 30l4 4 4-4c4-4-1-10-4-5Z", "#A33365")
        + oval(18.5, 35.5, 3.8, 1.9, "#F382A6") + oval(45.5, 35.5, 3.8, 1.9, "#F382A6")
        + line("M27 38q5 7 10 0", "#592451", 2.4),
    ),
    "hum": (
        "#D4ED99",
        "M14 34C10 22 17 12 29 9C41 7 51 14 51 23C51 35 43 36 43 42"
        "Q43 48 50 48Q57 49 58 42C63 52 49 57 40 54C33 52 27 57 21 51"
        "Q8 49 10 41Q11 37 14 34Z",
        fill("M13 42C19 48 28 42 35 46C42 51 49 54 57 49C52 55 47 56 40 54"
             "C33 52 27 57 21 51Q8 49 10 41Z", "#97C778")
        + line("M20 28q5 5 10 0m5 0q5 5 10 0M28 36q5 6 10 0", "#244744", 2.5),
    ),
    "psst": (
        "#A5E8DC",
        "M11 26C12 15 19 9 30 9C42 7 52 16 51 26Q50 33 55 38"
        "C61 46 51 53 43 50Q37 56 28 51Q15 56 6 47Q14 45 11 26Z",
        fill("M14 31C17 35 17 44 29 49L28 51Q15 56 6 47Q14 45 14 31Z", "#63BDAD")
        + oval(24, 27.5, 3.25, 4.6)
        + line("M34 28q5-6 10 0M25 38q4 5 10 0", "#17454D", 2.5)
        + fill("M45 48C39 47 35 44 35 40L34 33C34 29 38 29 39 32L41 39"
             "C48 34 54 40 50 45Q48 48 45 48Z", "#62BDA8"),
    ),
    "sigh": (
        "#DFECFF",
        "M11 41C15 34 11 27 14 20C17 11 26 8 35 9C44 9 50 16 50 24"
        "C50 34 56 39 55 46C55 52 50 53 46 51Q43 59 37 53"
        "Q30 60 24 53Q18 56 15 51Q7 50 11 41Z",
        fill("M14 46Q19 50 24 48Q30 54 37 49Q41 54 46 47Q52 50 55 46"
             "C55 52 50 53 46 51Q43 59 37 53Q30 60 24 53Q18 56 15 51Q10 50 14 46Z", "#BACEEB")
        + fill("M20 29q3 2 7 1v13q0 2-2 2h-3q-2 0-2-3Zm17 1q4 0 7-2l1 18q0 2-2 2h-3q-2 0-2-2Z", "#6DA0E6")
        + line("M19 26q5 5 10 3m6 0q5 2 10-3", "#294367", 2.5)
        + oval(32, 39, 2.6, 3.3, "#294367"),
    ),
    "jabber": (
        "#F4B9EC",
        "M13 29C12 20 17 13 25 10Q29 6 35 9C45 10 52 17 51 28L61 32"
        "L52 39L56 45L46 47L42 55L33 50L23 55L17 48L7 44L11 39L3 33Z",
        fill("M7 44L18 43L24 50L33 46L42 50L46 43L56 45L46 47L42 55L33 50L23 55L17 48Z", "#D78BCB")
        + star(24.5, 27.5, 6.1, "#482250") + star(39.5, 27.5, 6.1, "#482250")
        + fill("M22 38Q32 33 42 39Q40 49 32 49Q24 49 22 38Z", "#482250")
        + fill("M27 46q5-5 10 0q-5 4-10 0Z", "#F389AE"),
    ),
    "natter": (
        "#D7EAC4",
        "M12 15Q13 11 21 11L41 10Q51 10 51 19L52 33Q57 37 57 46"
        "Q56 53 48 53Q43 53 39 50Q33 55 29 51Q24 56 19 50Q9 55 6 48Q4 42 9 35Z",
        fill("M41 10Q51 10 51 19L52 33Q57 37 57 46Q56 53 48 53Q43 53 39 50"
             "Q43 44 44 36L44 21Q44 14 41 10Z", "#A0C598")
        + fill("M19 26h12q-1 5-6 5t-6-5Zm15 0h12q-1 5-6 5t-6-5Z", "#2C473A")
        + line("M27 38h10", "#2C473A", 2.8),
    ),
    "buzz": (
        "#FFCB67",
        "M11 29Q10 18 19 14L20 7L28 11L39 7L40 12Q50 15 51 27L59 24"
        "L55 35L61 42L50 43L46 54L37 48L31 57L23 48L13 52L13 43L3 41Z",
        fill("M3 41L14 37L17 46L24 42L32 50L37 43L46 48L50 38L61 42"
             "L50 43L46 54L37 48L31 57L23 48L13 52L13 43Z", "#E88C4E")
        + oval(23.5, 26, 4.1, 5.6, "#573045") + oval(39.5, 28, 2.5, 3.7, "#573045")
        + line("M19 20l5-2m14 4 5 2M24 39l4-4 4 4 3-3 4 3", "#573045", 2.55),
    ),
}


def ghost(name):
    body, contour, details = GHOSTS[name]
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">\n'
        f'  {fill(contour, body)}\n'
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
