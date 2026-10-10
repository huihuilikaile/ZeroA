#!/usr/bin/env python3
"""B2 图标 · 「2」竖直对位候选预览。

    python build/icons/preview_b2.py

同一个字标只改「2 落在哪一行」，各渲染一张 1024 大图 + 一张多尺寸对照表，
用来挑对位。渲染直接调 render_b2，所以预览所见即最终图标。

产物：
    build/icons/b2/variant-{baseline,center,top}-1024.png
    build/icons/b2/align-variants.png
"""

from __future__ import annotations

import sys
from pathlib import Path

from PIL import Image, ImageDraw

sys.path.insert(0, str(Path(__file__).resolve().parent))
import render_b2 as B  # noqa: E402

OUT = B.ICONS / "b2"

LABEL = {
    "baseline": "baseline 贴基线（像下标）",
    "center": "center 对 B 半高居中",
    "top": "top 贴字顶",
}

SIZES = (256, 128, 64, 32, 16)


def main():
    OUT.mkdir(parents=True, exist_ok=True)

    cols = {}
    for align in B.ALIGNS:
        big = B.render(1024, 4, align=align)
        big.save(OUT / f"variant-{align}-1024.png", optimize=True)
        cols[align] = [B.render(s, 8 if s <= 128 else 4, align=align) for s in SIZES]

    pad, gap, label_w = 28, 26, 240
    row_h = max(SIZES) + 16
    W = pad * 2 + label_w + sum(SIZES) + gap * (len(SIZES) - 1)
    H = pad * 2 + row_h * len(B.ALIGNS) + 30
    sheet = Image.new("RGB", (W, H), (138, 138, 142))
    d = ImageDraw.Draw(sheet)

    # 一条彩色竖条：图标圆角外是透明的，贴上去应该能看见底色
    stripe = Image.new("RGB", (W - pad * 2, 10), (255, 90, 90))
    sheet.paste(stripe, (pad, H - pad - 10))

    y = pad
    for align in B.ALIGNS:
        d.text((pad, y + row_h // 2 - 6), LABEL[align], fill=(20, 20, 22))
        x = pad + label_w
        base = y + row_h
        for s, im in zip(SIZES, cols[align]):
            sheet.paste(im, (x, base - s), im)
            d.text((x + s + 4, base - 8), str(s), fill=(40, 40, 44))
            x += s + gap
        y += row_h

    p = OUT / "align-variants.png"
    sheet.save(p)
    print("写出：")
    for align in B.ALIGNS:
        print(f"  {(OUT / f'variant-{align}-1024.png').relative_to(B.ROOT)}")
    print(f"  {p.relative_to(B.ROOT)}")


if __name__ == "__main__":
    main()
