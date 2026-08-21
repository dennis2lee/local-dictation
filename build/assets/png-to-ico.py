#!/usr/bin/env python3
"""Pack PNGs into a Windows .ico.

    python3 png-to-ico.py <png-dir> <out.ico>

An ICO is a small header plus the PNG bytes verbatim for sizes at or above
Vista, so no image library is needed — which keeps the Windows build from
depending on ImageMagick being installed.
"""

from __future__ import annotations

import struct
import sys
from pathlib import Path

SIZES = (16, 32, 48, 64, 128, 256)


def main() -> int:
    source = Path(sys.argv[1])
    target = Path(sys.argv[2])

    images = []
    for size in SIZES:
        candidate = source / f"icon-{size}.png"
        if candidate.is_file():
            images.append((size, candidate.read_bytes()))
    if not images:
        print(f"no icon-*.png under {source}", file=sys.stderr)
        return 1

    # ICONDIR: reserved, type 1 (icon), image count.
    header = struct.pack("<HHH", 0, 1, len(images))
    offset = len(header) + 16 * len(images)

    directory, payload = b"", b""
    for size, data in images:
        # 0 means 256 in the width/height byte.
        dimension = 0 if size >= 256 else size
        directory += struct.pack(
            "<BBBBHHII", dimension, dimension, 0, 0, 1, 32, len(data), offset
        )
        payload += data
        offset += len(data)

    target.write_bytes(header + directory + payload)
    print(f"  {target} ({len(images)} sizes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
