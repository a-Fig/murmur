"""Writes Murmur's ghost avatars (#579): six tones crossed with nine looks, 64x64 SVGs.

Usage: python scripts/ghosts.py internal/web/static/assets/ghosts
"""

import os
import sys

out = sys.argv[1]
os.makedirs(out, exist_ok=True)

# The tile behind the ghost. Muted, a step deeper than the ghost so it stands out on each.
TONES = {
    "night": "#2E3654",
    "dusk": "#7C6A9E",
    "sage": "#6F8C76",
    "sky": "#6E95BD",
    "clay": "#C0806A",
    "sand": "#B9A27A",
}

INK = "#232838"  # eyes and lines
BODY = "#F7F5F0"
SHADE = "#1B1F2C"  # the ghost's right side, at low opacity
BLUSH = "#E58E98"
BOW = "#D0697E"

# The ghost: a dome over straight sides, ending in three round scallops.
GHOST = (
    "M13 46V28C13 17.5 21.5 9 32 9C42.5 9 51 17.5 51 28V46"
    "C50.2 53.2 39.1 53.2 38.33 46C37.5 53.2 26.5 53.2 25.67 46"
    "C24.9 53.2 13.8 53.2 13 46Z"
)

EYE_L, EYE_R, EYE_Y = 25.5, 38.5, 28


def eye(x):
    return f'<ellipse cx="{x}" cy="{EYE_Y}" rx="3" ry="3.9" fill="{INK}"/>'


def lid(x):
    # A closed eye: a downward curve.
    return f'<path d="M{x - 2.8} {EYE_Y}Q{x} {EYE_Y + 2.9} {x + 2.8} {EYE_Y}" stroke="{INK}" stroke-width="1.7" stroke-linecap="round" fill="none"/>'


EYES = eye(EYE_L) + eye(EYE_R)

LOOKS = {
    "plain": EYES,
    "smile": EYES + f'<path d="M28.5 35.4Q32 38.8 35.5 35.4" stroke="{INK}" stroke-width="1.7" stroke-linecap="round" fill="none"/>',
    "sleepy": lid(EYE_L) + lid(EYE_R),
    "wink": eye(EYE_L) + lid(EYE_R),
    "blush": EYES
    + f'<circle cx="20.6" cy="34.2" r="2.8" fill="{BLUSH}" opacity=".7"/><circle cx="43.4" cy="34.2" r="2.8" fill="{BLUSH}" opacity=".7"/>',
    "oh": EYES + f'<ellipse cx="32" cy="36.6" rx="2" ry="2.4" fill="{INK}"/>',
    "glasses": EYES
    + f'<g stroke="{INK}" stroke-width="1.5" fill="none"><rect x="19.8" y="23.2" width="11.4" height="9.6" rx="2.6"/>'
    f'<rect x="32.8" y="23.2" width="11.4" height="9.6" rx="2.6"/><path d="M31.2 27H32.8"/></g>',
    "round-glasses": EYES
    + f'<g stroke="{INK}" stroke-width="1.5" fill="none"><circle cx="{EYE_L}" cy="{EYE_Y}" r="5.4"/>'
    f'<circle cx="{EYE_R}" cy="{EYE_Y}" r="5.4"/><path d="M30.9 27.2Q32 26.4 33.1 27.2"/></g>',
    "bow": EYES
    + f'<g fill="{BOW}" transform="translate(1 -3.6)"><path d="M41.4 16.2L35.6 12.4C34.8 11.9 33.8 12.5 33.9 13.4L34.5 19.2C34.6 20 35.5 20.4 36.2 20Z"/>'
    f'<path d="M41.4 16.2L47.8 13.6C48.7 13.2 49.6 14 49.3 14.9L47.5 20.4C47.2 21.2 46.2 21.4 45.6 20.9Z"/>'
    f'<circle cx="41.4" cy="16.4" r="2.3"/></g>',
}


def ghost(tone, look):
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">'
        f'<defs><clipPath id="g"><path d="{GHOST}"/></clipPath></defs>'
        f'<circle cx="32" cy="32" r="32" fill="{TONES[tone]}"/>'
        '<ellipse cx="32" cy="57" rx="12.5" ry="2" fill="#000" opacity=".16"/>'
        f'<path d="{GHOST}" fill="{BODY}"/>'
        f'<g clip-path="url(#g)"><path d="M41 5C48 10 51 18 51 28V56H58V5Z" fill="{SHADE}" opacity=".08"/></g>'
        f"{LOOKS[look]}</svg>\n"
    )


names = []
for tone in TONES:
    for look in LOOKS:
        name = f"{tone}-{look}"
        with open(os.path.join(out, name + ".svg"), "w", encoding="utf-8", newline="\n") as f:
            f.write(ghost(tone, look))
        names.append(name)
print(len(names))
