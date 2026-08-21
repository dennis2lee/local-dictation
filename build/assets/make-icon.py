#!/usr/bin/env python3
"""Generate the application icon.

Written by hand with zlib rather than pulled from a design tool so the build has
no binary blobs in version control and no image library to install: the icon is
reproducible from this file alone.

    python3 make-icon.py out/            # writes icon-<size>.png for every size

The glyph is a microphone capsule over an arc and a stand — the shape people
already read as "dictation" — on a rounded square in the accent colour.
"""

from __future__ import annotations

import math
import struct
import sys
import zlib
from pathlib import Path

SIZES = (16, 32, 64, 128, 256, 512, 1024)

BACKGROUND = (0x1F, 0x2A, 0x37, 0xFF)   # slate, reads on light and dark docks
ACCENT = (0x4C, 0xC2, 0x8C, 0xFF)       # the same green as the "listening" LED
GLYPH = (0xF4, 0xF6, 0xF8, 0xFF)


def blend(bottom, top, alpha):
    return tuple(round(b + (t - b) * alpha) for b, t in zip(bottom[:3], top[:3])) + (0xFF,)


def rounded_square_alpha(x, y, size, radius):
    """Signed-distance coverage for a rounded square, antialiased."""
    half = size / 2
    px, py = abs(x - half), abs(y - half)
    inner = half - radius
    dx, dy = max(px - inner, 0.0), max(py - inner, 0.0)
    distance = math.hypot(dx, dy) - radius
    return clamp(0.5 - distance)


def capsule_alpha(x, y, cx, top, bottom, radius):
    """Coverage for a vertical capsule (the microphone body)."""
    cy = min(max(y, top), bottom)
    return clamp(0.5 - (math.hypot(x - cx, y - cy) - radius))


def arc_alpha(x, y, cx, cy, radius, thickness):
    """Coverage for the lower half of a ring (the microphone cradle)."""
    if y < cy:
        return 0.0
    return clamp(0.5 - (abs(math.hypot(x - cx, y - cy) - radius) - thickness / 2))


def bar_alpha(x, y, cx, top, bottom, half_width):
    """Coverage for the stand and its foot."""
    inside_x = half_width - abs(x - cx)
    inside_y = min(y - top, bottom - y)
    return clamp(0.5 + min(inside_x, inside_y))


def clamp(value):
    return 0.0 if value < 0 else 1.0 if value > 1 else value


def render(size):
    scale = size / 1024
    radius = 220 * scale

    body_cx = 512 * scale
    body_top, body_bottom = 250 * scale, 520 * scale
    body_radius = 108 * scale

    cradle_radius = 210 * scale
    cradle_thickness = 56 * scale

    stand_top, stand_bottom = 690 * scale, 790 * scale
    foot_top, foot_bottom = 780 * scale, 812 * scale

    # Supersample small icons: at 16 px a single sample per pixel turns the
    # glyph into noise.
    samples = 4 if size <= 128 else 2
    step = 1.0 / samples

    rows = []
    for pixel_y in range(size):
        row = bytearray()
        for pixel_x in range(size):
            accumulated = [0.0, 0.0, 0.0, 0.0]
            for sy in range(samples):
                for sx in range(samples):
                    x = pixel_x + (sx + 0.5) * step
                    y = pixel_y + (sy + 0.5) * step

                    outside = rounded_square_alpha(x, y, size, radius)
                    if outside <= 0:
                        continue

                    colour = BACKGROUND
                    glyph = max(
                        capsule_alpha(x, y, body_cx, body_top, body_bottom, body_radius),
                        arc_alpha(x, y, body_cx, 470 * scale, cradle_radius, cradle_thickness),
                        bar_alpha(x, y, body_cx, stand_top, stand_bottom, 32 * scale),
                        bar_alpha(x, y, body_cx, foot_top, foot_bottom, 160 * scale),
                    )
                    if glyph > 0:
                        colour = blend(colour, GLYPH, glyph)

                    # A thin accent ring, so the icon still reads at 16 px.
                    ring = rounded_square_alpha(x, y, size, radius) - rounded_square_alpha(
                        x, y, size, radius - max(1.0, 18 * scale)
                    )
                    if ring > 0:
                        colour = blend(colour, ACCENT, min(ring, 1.0) * 0.9)

                    for channel in range(3):
                        accumulated[channel] += colour[channel] * outside
                    accumulated[3] += outside

            total = samples * samples
            alpha = accumulated[3] / total
            if alpha <= 0:
                row += bytes(4)
                continue
            row += bytes(
                round(accumulated[channel] / accumulated[3]) for channel in range(3)
            ) + bytes([round(alpha * 255)])
        rows.append(bytes(row))
    return rows


def write_png(path: Path, size: int, rows: list[bytes]) -> None:
    raw = b"".join(b"\x00" + row for row in rows)

    def chunk(tag: bytes, payload: bytes) -> bytes:
        return (
            struct.pack(">I", len(payload))
            + tag
            + payload
            + struct.pack(">I", zlib.crc32(tag + payload) & 0xFFFFFFFF)
        )

    png = b"\x89PNG\r\n\x1a\n"
    png += chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(raw, 9))
    png += chunk(b"IEND", b"")
    path.write_bytes(png)


def main() -> int:
    out = Path(sys.argv[1] if len(sys.argv) > 1 else "out")
    out.mkdir(parents=True, exist_ok=True)
    for size in SIZES:
        write_png(out / f"icon-{size}.png", size, render(size))
        print(f"  {out / f'icon-{size}.png'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
