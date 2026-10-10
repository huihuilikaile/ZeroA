#!/usr/bin/env python3
"""生成应用图标：纯黑圆角方块 + 纯白「B2」字标（2 缩到一半高、放在 B 右边）。

    python build/icons/render_b2.py

需求逐条落实
------------
  * 文本 B2          → 字标只有 B 和 2 两个字，几何直接画出来，不依赖任何字体
  * 2 缩小放在右边    → 2 整体缩到 0.5 倍，贴 B 右侧（间距 GAP）
  * 不超过 B 的一半   → 2 的字高 = B 大写高度的一半（正好一半，即上限；见 TWO_SCALE）
  * 纯白色字         → #FFFFFF（不是 tokens.css 的 --text #F9FAFB）
  * 黑色背景         → #000000（不是 --bg #151517）
  * 带圆角           → 底座沿用 render_icons.py 的 squircle（超椭圆 n=5），
                       所以新图标跟旧蝴蝶图标的**外轮廓完全一致**，只换中间的字标

字形怎么来的
------------
没有用系统字体（换台机器字体不一样，图标就不是同一个图标了），B 和 2 都是几何画出来的：

* B = 竖干 + 上碗 + 下碗，碗上挖掉一块"内白"（counter）。
  碗的右轮廓是一段从平边（水平切向）一直走到收腰点的圆弧——注意不是半圆：
  半圆的右端在收腰处是水平切向的，两个半圆拼起来会在腰上切出一条水平细缝，
  看着像"Ƀ"。让圆弧斜着（≈35°）落到收腰点，腰才是一个正常的 V 形凹口。
  内白左边不内缩——它贴着竖干右缘，所以竖干粗细 = B_STEM，
  而横笔画（碗的上下缘）= S_H、右端竖笔画 = S_V。
  印刷惯例：横笔画要比竖笔画细、竖干最粗，所以 S_H < S_V < B_STEM。
  两个碗在收腰处共用同一个点（WAIST_X, WAIST），轮廓才连续、不会出现台阶。
* 2 = 上钩（一段圆弧）+ 斜笔 + 底横。钩的起笔（圆弧最左端）圆头，
  斜笔两端圆头；斜笔落点的圆头正好与基线相切，所以 2 的字高卡在整 100。

2 的笔画看起来比 B 细，但**没有细到一半**：2 自己的笔画是 0.24 字高，
B 的竖干是 0.20 字高，缩到一半后 2 的绝对笔画 = 0.12（= B 的 60%）。
字越小越要相对加粗（光学补偿），按字面等比缩会显得发飘。

产物
----
  build/icons/b2-mark.svg       母版（矢量，可再编辑）
  build/icons/icon-16..1024.png 各尺寸 PNG（真透明圆角外）
  build/appicon.png             Wails 图标源（1024），喂给
                                `wails3 generate icons -input appicon.png ...`
                                （.ico/.icns 由那条命令派生，本脚本不碰）

旧图标没被删：build/icons/pi-mark.svg + render_icons.py 还在，
`python build/icons/render_icons.py` 就能把蝴蝶那版原样重刷回来。

只用 numpy + Pillow，纯本地计算，不联网、不起子进程。
"""

from __future__ import annotations

import math
import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

sys.path.insert(0, str(Path(__file__).resolve().parent))
import render_icons as R  # noqa: E402  复用底座几何：VB / PAD / SQUIRCLE_N / tile_alpha

ROOT = Path(__file__).resolve().parents[2]
BUILD = ROOT / "build"
ICONS = BUILD / "icons"

VB = R.VB                    # 120：跟旧图标同一套画布坐标
BG = (0x00, 0x00, 0x00)      # 纯黑底
FG = (0xFF, 0xFF, 0xFF)      # 纯白字

# ---------------------------------------------------------------- 字标参数
# 设计坐标：B 的基线在 y=CAP，字顶在 y=0，所以 B 的大写高度就是 CAP。

CAP = 100.0
MARK_FIT = 0.80              # 字标较长边占画布的比例（留白 10% 一圈）

# —— B ——
B_STEM = 20.0                # 竖干粗细（最粗）
WAIST = 45.0                 # 上下碗的分界高度（收腰）
WAIST_X = 55.0               # 上下碗共用的收腰点 x：轮廓在这里交汇成 V 形腰
S_H = 12.0                   # 碗的横笔画（上缘/下缘）粗细
S_V = 16.0                   # 碗右端的竖笔画粗细
UP_T = 33.5                  # 上碗平边的终点（圆弧起点）
LO_T = 32.0                  # 下碗平边的终点（圆弧起点）

# —— 2（先在"字高 100"自己的坐标系里画，最后整体 × TWO_SCALE）——
TWO_CAP = 100.0
TWO_W = 64.0                 # 2 的宽度
TWO_R = 20.0                 # 上钩的圆心线半径
TWO_STROKE = 24.0            # 钩与斜笔的粗细（相对 2 自己的字高）
TWO_BAR = 16.0               # 底横粗细（横笔画照惯例比竖笔画细）
TWO_A0, TWO_A1 = 180.0, 352.0   # 钩的起止角（y 向下：180=左，270=上，360=右）
TWO_BAR_X0 = 10.0            # 底横左端
TWO_FOOT_X = 22.0            # 斜笔落点 x（圆头下缘正好切到基线）

# 需求里的硬约束：2 不超过 B 的一半。这里取**正好一半**（用满上限），
# verify_b2.py 会从渲染出来的像素反测这个比例。
TWO_SCALE = 0.5

GAP = 15.0                   # B 和 2 的水平间距

# 2 的竖直对位：baseline（贴基线，像下标）/ center（对 B 的半高居中）/ top（贴字顶）
ALIGN = "baseline"
ALIGNS = ("baseline", "center", "top")

# 逐尺寸生成参数：(边长, 超采样倍数)。小尺寸多超采样，边缘才干净。
SIZES = ((1024, 4), (512, 4), (256, 8), (128, 8), (64, 8), (48, 8), (32, 16), (24, 16), (16, 16))


# ---------------------------------------------------------------- 几何工具

def arc_pts(cx, cy, r, a0, a1, steps):
    """圆弧采样。角度用度、y 向下：180=左，270=上，0/360=右。"""
    return [
        (cx + r * math.cos(math.radians(a)), cy + r * math.sin(math.radians(a)))
        for a in (a0 + (a1 - a0) * i / steps for i in range(steps + 1))
    ]


def circle(cx, cy, r, steps=120):
    return arc_pts(cx, cy, r, 0.0, 360.0, steps)[:-1]


def rounded_rect(x0, y0, x1, y1, radii, steps=48):
    """圆角矩形轮廓。radii = (左上, 右上, 右下, 左下)，0 表示直角。

    从左上角起、顺时针（屏幕坐标）走一圈；圆角用圆弧采样。
    """
    tl, tr, br, bl = radii
    pts: list[tuple[float, float]] = []
    pts += arc_pts(x0 + tl, y0 + tl, tl, 180.0, 270.0, steps) if tl else [(x0, y0)]
    pts += arc_pts(x1 - tr, y0 + tr, tr, 270.0, 360.0, steps) if tr else [(x1, y0)]
    pts += arc_pts(x1 - br, y1 - br, br, 0.0, 90.0, steps) if br else [(x1, y1)]
    pts += arc_pts(x0 + bl, y1 - bl, bl, 90.0, 180.0, steps) if bl else [(x0, y1)]
    return pts


def quad(p0, p1, width):
    """沿 p0→p1 的一条带宽矩形（平头笔画）。"""
    dx, dy = p1[0] - p0[0], p1[1] - p0[1]
    ln = math.hypot(dx, dy) or 1.0
    nx, ny = -dy / ln * width / 2.0, dx / ln * width / 2.0
    return [
        (p0[0] + nx, p0[1] + ny),
        (p1[0] + nx, p1[1] + ny),
        (p1[0] - nx, p1[1] - ny),
        (p0[0] - nx, p0[1] - ny),
    ]


def signed_area(poly):
    s = 0.0
    for i, (x, y) in enumerate(poly):
        x2, y2 = poly[(i + 1) % len(poly)]
        s += x * y2 - x2 * y
    return s / 2.0


def wind(poly, ccw=True):
    """把多边形统一成指定绕向（SVG 的 nonzero 填充靠这个区分外轮廓与内白）。"""
    return poly if (signed_area(poly) > 0) == ccw else poly[::-1]


# ---------------------------------------------------------------- B

def _bowl(t, sign):
    """一个碗的 (外轮廓, 内白)。

    sign=+1 上碗（平边在字顶 y=0）、sign=-1 下碗（平边在字底 y=CAP）。
    平边水平切向进入圆弧，圆弧一路走到收腰点 (WAIST_X, WAIST)；
    圆弧半径由"过平边终点 + 过收腰点"两个条件解出来（切向水平 → 圆心在平边终点正下方）。
    """
    y_edge = 0.0 if sign > 0 else CAP
    h = abs(WAIST - y_edge)                     # 碗高
    dx = WAIST_X - t                            # 平边终点到收腰点的水平距离
    r = (dx * dx + h * h) / (2.0 * h)           # 外轮廓弧半径
    cy = y_edge + sign * r                      # 圆心

    a0 = 270.0 if sign > 0 else 90.0            # 平边终点处的角度（切向水平）
    a1 = math.degrees(math.atan2(WAIST - cy, dx))
    if sign > 0 and a1 < a0:
        a1 += 360.0
    if sign < 0 and a1 > a0:
        a1 -= 360.0

    outer = (
        [(B_STEM, y_edge), (t, y_edge)]
        + arc_pts(t, cy, r, a0, a1, 240)
        + [(B_STEM, WAIST)]
    )

    # 内白：平边内缩 S_H，右端最远点内缩 S_V（贴着竖干那边不内缩）
    y_c = y_edge + sign * S_H                   # 内白的平边
    y_w = WAIST - sign * S_H                    # 内白靠腰的那条边
    hc = abs(y_w - y_c)
    m = t + r - S_V                             # 内白最右
    rc = min(hc, m - B_STEM)
    tc = m - rc
    x_end = tc + math.sqrt(max(rc * rc - (hc - rc) ** 2, 0.0))
    cyc = y_c + sign * rc

    b0 = 270.0 if sign > 0 else 90.0
    b1 = math.degrees(math.atan2(y_w - cyc, x_end - tc))
    if sign > 0 and b1 < b0:
        b1 += 360.0
    if sign < 0 and b1 > b0:
        b1 -= 360.0

    inner = (
        [(B_STEM, y_c), (tc, y_c)]
        + arc_pts(tc, cyc, rc, b0, b1, 240)
        + [(B_STEM, y_w)]
    )
    return outer, inner


def b_polys():
    """B 的 (外轮廓, 内白)。外轮廓三块在 x=B_STEM 处相接、互不重叠。"""
    outer, inner = [], []
    for t, sign in ((UP_T, 1.0), (LO_T, -1.0)):
        o, i = _bowl(t, sign)
        outer.append(o)
        inner.append(i)
    outer.insert(0, rounded_rect(0.0, 0.0, B_STEM, CAP, (0, 0, 0, 0)))   # 竖干
    return outer, inner


def b_right():
    """B 最右的 x（供 2 定位）。"""
    outer, _ = b_polys()
    return max(p[0] for poly in outer for p in poly)


# ---------------------------------------------------------------- 2

def two_polys():
    """2 的外轮廓（自己的坐标系：字高 100、宽 TWO_W）。"""
    half = TWO_STROKE / 2.0
    cx = TWO_R + half                       # 圆心：钩最左端的外缘落在 x=0
    cy = TWO_R + half                       # 钩最上端的外缘落在 y=0
    a0, a1 = TWO_A0, TWO_A1

    end = (
        cx + TWO_R * math.cos(math.radians(a1)),
        cy + TWO_R * math.sin(math.radians(a1)),
    )
    foot = (TWO_FOOT_X, TWO_CAP - half)     # 圆头下缘正好与基线相切
    bar_y1 = TWO_CAP

    polys = [
        # 上钩：外弧 + 内弧围成的月牙
        arc_pts(cx, cy, TWO_R + half, a0, a1, 160)
        + arc_pts(cx, cy, TWO_R - half, a1, a0, 160),
        # 钩起笔 / 收笔的圆头
        circle(cx + TWO_R * math.cos(math.radians(a0)),
               cy + TWO_R * math.sin(math.radians(a0)), half),
        circle(end[0], end[1], half),
        # 斜笔（平头带 + 两端圆头）
        quad(end, foot, TWO_STROKE),
        circle(foot[0], foot[1], half),
        # 底横
        rounded_rect(TWO_BAR_X0, bar_y1 - TWO_BAR, TWO_W, bar_y1, (0, 0, 2, 2)),
    ]
    return polys


# ---------------------------------------------------------------- 字标（B + 2）

def two_offset_y(align):
    """2 相对基线的落点（设计坐标，y 向下）。"""
    h = TWO_CAP * TWO_SCALE
    if align == "baseline":
        return CAP - h
    if align == "center":
        return (CAP - h) / 2.0
    if align == "top":
        return 0.0
    raise ValueError(f"未知对位：{align}")


def mark_polys(align=ALIGN):
    """整个字标的 (填充, 挖空) 多边形，坐标在设计坐标里（B 的大写高度 = CAP）。"""
    outer, inner = b_polys()
    dx = b_right() + GAP
    dy = two_offset_y(align)
    s = TWO_SCALE
    outer = outer + [
        [(dx + s * x, dy + s * y) for x, y in poly] for poly in two_polys()
    ]
    return outer, inner


def mark_bounds(align=ALIGN):
    pts = [p for poly in mark_polys(align)[0] for p in poly]
    xs = [p[0] for p in pts]
    ys = [p[1] for p in pts]
    return min(xs), min(ys), max(xs), max(ys)


def placement(align=ALIGN):
    """字标 → 画布：等比缩放 + 居中，较长边占 MARK_FIT。"""
    x0, y0, x1, y1 = mark_bounds(align)
    w, h = x1 - x0, y1 - y0
    scale = VB * MARK_FIT / max(w, h)
    tx = (VB - scale * w) / 2.0 - scale * x0
    ty = (VB - scale * h) / 2.0 - scale * y0
    return scale, tx, ty


# ---------------------------------------------------------------- 渲染

def _to_px(poly, scale, tx, ty, f):
    return [((tx + scale * x) * f, (ty + scale * y) * f) for x, y in poly]


def glyph_mask(size, ss, align=ALIGN):
    """字标的 alpha 掩码（L 图，size*ss 分辨率，硬边；抗锯齿交给最后的缩放）。"""
    n = size * ss
    f = n / VB
    scale, tx, ty = placement(align)
    outer, inner = mark_polys(align)

    img = Image.new("L", (n, n), 0)
    d = ImageDraw.Draw(img)
    for poly in outer:
        d.polygon(_to_px(poly, scale, tx, ty, f), fill=255)
    for poly in inner:                      # 内白：直接覆盖成 0（外轮廓互不重叠，安全）
        d.polygon(_to_px(poly, scale, tx, ty, f), fill=0)
    return img


def render(size, ss, align=ALIGN):
    """渲染一张 size×size 的 RGBA 图标（黑底白字，圆角外 alpha=0）。"""
    n = size * ss
    mask = glyph_mask(size, ss, align)

    # 底座：跟旧图标同一套 squircle 几何（render_icons.tile_alpha）
    ta = np.clip(np.rint(R.tile_alpha(size, ss) * 255.0), 0, 255).astype(np.uint8)
    tile = Image.fromarray(ta, "L")

    # 背景是纯黑、字是纯白 → 亮度通道就是掩码本身，用 LA 省内存
    la = Image.merge("LA", (mask, tile))
    if ss > 1:
        la = la.resize((size, size), Image.LANCZOS)
    return la.convert("RGBA")


# ---------------------------------------------------------------- SVG 母版

def _fmt(v):
    return f"{v:.2f}".rstrip("0").rstrip(".")


def _path(prefix, polys, ccw):
    out = []
    for poly in polys:
        pts = wind(poly, ccw=ccw)
        d = "M" + "L".join(f"{_fmt(x)} {_fmt(y)}" for x, y in pts) + "Z"
        out.append(prefix + d)
    return " ".join(out)


def write_svg(align=ALIGN):
    scale, tx, ty = placement(align)

    def T(polys):
        return [[(tx + scale * x, ty + scale * y) for x, y in poly] for poly in polys]

    outer, inner = mark_polys(align)
    # 外轮廓与外轮廓同向（nonzero 填充下重叠即并集），内白反向 → 被挖成洞
    d = _path("", T(outer), ccw=True) + " " + _path("", T(inner), ccw=False)

    a = VB / 2.0 * (1.0 - R.PAD)
    tile = " ".join(f"{_fmt(x)} {_fmt(y)}" for x, y in R.squircle_path(a))

    svg = f"""<?xml version="1.0" encoding="UTF-8"?>
<!--
  B2 应用图标（母版 · viewBox {VB:g}x{VB:g}）
  ========================================
  字标：纯白 #FFFFFF 的「B2」，2 缩到 B 的一半高、放在右侧；
  底座：纯黑 #000000，圆角是 squircle（超椭圆 n={R.SQUIRCLE_N:g}），方块之外全透明。
  底座几何与 render_icons.py（蝴蝶那版）完全一致，所以新旧图标外轮廓相同。

  B 由竖干 + 上下两个碗构成（碗的右轮廓是一直走到收腰点的圆弧），碗里挖内白；
  2 由上钩 + 斜笔 + 底横构成。
  全部是几何轮廓（多边形），不依赖任何字体；多边形点由 render_b2.py 生成：
      python build/icons/render_b2.py
  -->
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {VB:g} {VB:g}" width="{VB:g}" height="{VB:g}">
  <title>B2</title>

  <!-- 圆角底座：squircle，外面保持透明 -->
  <path fill="#{BG[0]:02X}{BG[1]:02X}{BG[2]:02X}" d="M{tile} Z" />

  <!-- 字标：外轮廓同向 + 内白反向，nonzero 填充 -->
  <path fill="#{FG[0]:02X}{FG[1]:02X}{FG[2]:02X}" fill-rule="nonzero" d="{d}" />
</svg>
"""
    p = ICONS / "b2-mark.svg"
    p.write_text(svg, encoding="utf-8")
    return p


# ---------------------------------------------------------------- 自检 / 主流程

def measure(im):
    """从渲染结果反测：字标外接框、B 与 2 各自的高度（按列空隙把两个字分开）。"""
    a = np.asarray(im)[..., 3]
    ys, xs = np.nonzero(a > 127)
    if len(xs) == 0:
        return None
    box = (int(xs.min()), int(ys.min()), int(xs.max()), int(ys.max()))

    # 只保留字标（白色像素），按列统计再找中间的空隙，把 B 和 2 分开
    ink = (np.asarray(im)[..., 0] > 127) & (a > 127)
    cols = ink.any(axis=0)
    idx = np.nonzero(cols)[0]
    gaps = [i for i in range(idx[0], idx[-1]) if not cols[i]]
    if not gaps:
        return box, None
    cut = gaps[len(gaps) // 2]
    left = ink[:, :cut + 1]
    right = ink[:, cut + 1:]

    def h(mask):
        yy = np.nonzero(mask.any(axis=1))[0]
        return int(yy.max() - yy.min() + 1) if len(yy) else 0

    return box, (h(left), h(right))


def report(images, align=ALIGN):
    scale, tx, ty = placement(align)
    x0, y0, x1, y1 = mark_bounds(align)
    print(f"字标设计框   : x {x0:.1f}..{x1:.1f}  y {y0:.1f}..{y1:.1f}（宽 {x1-x0:.1f} × 高 {y1-y0:.1f}）")
    print(f"放置         : scale={scale:.4f}  translate=({tx:.2f}, {ty:.2f})  对位={align}")
    print(f"2 的字高     : {TWO_CAP * TWO_SCALE:.1f} = B 的一半（{TWO_CAP:.1f}×{TWO_SCALE:g}）")
    print()
    print("尺寸     圆角外透明   中心不透明   字标外接框           B高/2高")
    for size in sorted(images, reverse=True):
        im = images[size]
        a = np.asarray(im)[..., 3]
        m = measure(im)
        if m is None:
            print(f"{size:>5}    <全空!>")
            continue
        box, hs = m
        corner = int(a[0, 0])
        center = int(a[size // 2, size // 2])
        ratio = f"{hs[1] / hs[0]:.3f}" if hs else "n/a"
        print(
            f"{size:>5}   {corner:>3}          {center:>3}          "
            f"{str(box):<18} {hs[0] if hs else 0:>4}/{hs[1] if hs else 0:<4} = {ratio}"
        )


def main():
    import argparse

    ap = argparse.ArgumentParser(description="生成 B2 应用图标")
    ap.add_argument("--align", choices=ALIGNS, default=ALIGN,
                    help="2 的竖直对位（默认 %(default)s）")
    args = ap.parse_args()
    align = args.align

    ICONS.mkdir(parents=True, exist_ok=True)

    svg = write_svg(align)
    images = {}
    for size, ss in SIZES:
        img = render(size, ss, align=align)
        images[size] = img
        img.save(ICONS / f"icon-{size}.png", optimize=True)

    # Wails 的图标源
    images[1024].save(BUILD / "appicon.png", optimize=True)

    print("生成：")
    print(f"  {svg.relative_to(ROOT)}  （母版）")
    for size, _ in SIZES:
        p = ICONS / f"icon-{size}.png"
        print(f"  {p.relative_to(ROOT)}  ({p.stat().st_size:,} B)")
    p = BUILD / "appicon.png"
    print(f"  {p.relative_to(ROOT)}  ({p.stat().st_size:,} B)  ← Wails 图标源")
    print()
    print("下一步（.ico 由项目流水线从母版派生）：")
    print("  cd build && wails3 generate icons -input appicon.png \\")
    print("      -windowsfilename windows/icon.ico -macfilename darwin/icon.icns")
    print()
    report(images, align)


if __name__ == "__main__":
    main()
