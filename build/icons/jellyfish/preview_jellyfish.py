#!/usr/bin/env python3
"""「水母 π」设计探索表 —— 一次看 8 个变体，用来挑方案。

    python build/icons/jellyfish/preview_jellyfish.py

每行一个变体，列依次是：
  256 深色底座 / 256 图形（棋盘格=真透明）/ 256 浅色底座 / 64 / 32 / 16
右边小尺寸那几格是给"缩到任务栏还认不认得出"看的。

产物：variants-dark.png（深色版式）、variants-light.png（浅色版式）
这两个只是评审物，不是产品图标。
"""

from __future__ import annotations

import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import render_jellyfish as R  # noqa: E402

BIG = 256
SMALLS = (64, 32, 16)
LABEL_W = 210
PAD = 26
GAP = 30
LABEL_H = 34


def checker(size, sq=16, a=(58, 58, 62), b=(38, 38, 41)):
    img = Image.new("RGB", (size, size), a)
    d = ImageDraw.Draw(img)
    for y in range(0, size, sq):
        for x in range(0, size, sq):
            if (x // sq + y // sq) % 2:
                d.rectangle([x, y, x + sq - 1, y + sq - 1], fill=b)
    return img


def sheet(dark: bool, variants, tag: str):
    page_bg = (22, 22, 25) if dark else (245, 245, 246)
    ink = (196, 196, 202) if dark else (58, 58, 64)
    dim = (140, 140, 148) if dark else (120, 120, 128)

    n = len(variants)
    dark_panel = (28, 28, 31) if dark else (236, 236, 238)

    W = PAD + LABEL_W + BIG * 3 + GAP * 3 + SMALLS[0] + GAP + SMALLS[1] + GAP + SMALLS[2] + PAD
    H = PAD + n * (BIG + LABEL_H + PAD)
    canvas = Image.new("RGB", (W, H), page_bg)
    d = ImageDraw.Draw(canvas)

    for i, v in enumerate(variants):
        top = PAD + i * (BIG + LABEL_H + PAD)
        base = top + BIG                      # 小尺寸按底边对齐到大图的底线
        bar_w = v.stroke_w if v.bar_w is None else v.bar_w
        d.text((PAD, top + 10), v.name, fill=ink)
        d.text((PAD, top + 32), f"转{v.rotate_deg:g}° 切{v.slant_deg:g}° "
                                f"杠{bar_w:g} 腿{v.stroke_w:g} {v.leg_style}", fill=dim)
        d.text((PAD, top + 52), v.note[:24], fill=dim)

        x = PAD + LABEL_W

        # 1) 深色底座
        panel = Image.new("RGB", (BIG, BIG), dark_panel)
        tile = R.render(BIG, 2, v, "tile", dark=True)
        panel.paste(tile, (0, 0), tile)
        canvas.paste(panel, (x, top))
        x += BIG + GAP

        # 2) 图形 + 棋盘格（证明是透明背景，不是白底）
        chk = checker(BIG)
        mark = R.render(BIG, 2, v, "mark", dark=True)
        chk.paste(mark, (0, 0), mark)
        canvas.paste(chk, (x, top))
        x += BIG + GAP

        # 3) 浅色底座
        panel = Image.new("RGB", (BIG, BIG), (255, 255, 255))
        tile_l = R.render(BIG, 2, v, "tile", dark=False)
        panel.paste(tile_l, (0, 0), tile_l)
        canvas.paste(panel, (x, top))
        x += BIG + GAP

        # 4) 真实小尺寸
        for s in SMALLS:
            im = R.render(s, 4 if s > 16 else 16, v, "tile", dark=True)
            canvas.paste(im, (x, base - s), im)
            d.text((x + max(0, s // 2 - 6), base + 8), str(s), fill=dim)
            x += s + GAP

    out = HERE / f"variants{tag}-{'dark' if dark else 'light'}.png"
    canvas.save(out)
    return out


def main():
    for tag, variants in (("", R.VARIANTS), ("2", R.VARIANTS_2), ("3", R.VARIANTS_3),
                          ("4", R.VARIANTS_4)):
        for dark in (True, False):
            p = sheet(dark, variants, tag)
            print(f"生成 {p.name}  ({p.stat().st_size:,} B)")


if __name__ == "__main__":
    main()
