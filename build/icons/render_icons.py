#!/usr/bin/env python3
"""生成应用图标（Pi Agent Workspace）—— 一只用简单线条画的蝴蝶。

图形来源
--------
frontend/src/features/shell/StartupSplash.tsx 里的启动动画：一只蝴蝶
（两条曲线当翅膀）+ 右上角一个实心圆点。启动动画的原始曲线是左右不对称的，
画到 120×120 里更像一个抽象的 X，所以图标专门画了一版**左右对称的蝴蝶**：
四片翅膀（上翅 / 下翅各一对，每条翅膀两笔勾出轮廓）、一根竖直的身体、
一对向上张开的触角。线条粗细、圆端帽、圆拐角都跟启动动画保持一致。

方案是拿 preview_butterfly.py 出的六版预览挑的（F 方案：四片翅膀 + 身体）。

产物
----
  build/icons/pi-mark.svg      母版（矢量，可再编辑）
  build/icons/icon-1024.png    1024 母版（透明背景 + 圆角底）
  build/icons/icon-512/256/128/64/48/32/24/16.png
  build/appicon.png            Wails 的图标源（1024，Taskfile generate:icons 吃这个）

  build/windows/icon.ico 与 build/darwin/icon.icns 由项目自己的流水线生成，
  不在这个脚本里产出——不然会和 `wails3 generate icons`（Taskfile 的
  generate:icons 任务）互相覆盖：
      cd build && wails3 generate icons -input appicon.png \
          -windowsfilename windows/icon.ico -macfilename darwin/icon.icns
  本脚本只负责把母版 PNG 做对，.ico/.icns 交给 wails3 从母版派生。

设计要点
--------
* 底色 #151517 = tokens.css 的 --bg（暗色主题）；标记 #f9fafb = --text。
* 圆角走 squircle（超椭圆 |x/a|^n + |y/b|^n = 1，n=5），
  不是正圆角矩形——角度连续、更像系统级应用图标。
* 圆角方块之外全部 alpha=0（真透明），不做白底。
* 画笔走"到折线的精确距离场"而不是 PIL 的折线扫描：圆端帽 / 圆拐角数学上精确，
  放大后也不会有梳齿状条纹。
* 所有尺寸都是矢量重新栅格化 + 4x 超采样，不是把小图放大。

这个脚本只用 numpy + Pillow，纯本地计算，不联网、不起子进程。
"""

from __future__ import annotations

import math
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

# ---------------------------------------------------------------- 基本参数

ROOT = Path(__file__).resolve().parents[2]          # 仓库根
BUILD = ROOT / "build"
ICONS = BUILD / "icons"

VB = 120.0              # 母版画布边长
VB_ART = 100.0          # 蝴蝶的画布（图形坐标定义在 0..100 里）
SS = 4                  # 超采样倍数
SQUIRCLE_N = 5.0        # 超椭圆指数：5 ≈ squircle（越大越方）
# 圆角方块四周留一圈透明边：铺满画布时 16px 上圆角会糊成直角，
# 留 4.5% 之后小尺寸也看得出来是圆角，同时也更像一枚"图标"而不是一整块色板。
PAD = 0.045

BG = (0x15, 0x15, 0x17)     # tokens.css --bg（暗色）
FG = (0xF9, 0xFA, 0xFB)     # tokens.css --text（暗色）

STROKE_W = 5.5              # 笔画粗细（跟启动动画同一个量级）
FLATTEN_STEPS = 160         # 每条曲线采样成多少段折线（偏差约 1/500 单位，看不见）
AA_WIDTH = 1.0              # 距离场 → 覆盖率的过渡带宽度（渲染像素）
CHUNK = 512                 # 距离场的分带高度（渲染像素）
SEG_CHUNK = 64              # 一次算多少段折线（控制临时数组大小）

# 标记在画布里的额外缩放系数：定完占比后再乘一下（微调视觉重量）
FIT = 0.94

# ---------------------------------------------------------------- 蝴蝶（侧飞）
#
# 坐标都在 0..120 的画布坐标里。侧飞的蝴蝶 = 一长一短两笔 + 一个身体圆点：
#   * 短笔偏竖   → 前翅（扇起来的那片）
#   * 长笔偏平   → 后翅（拖在后面、更长的那片）
#   * 两笔交在胸部，那里点一个实心圆当身体
# 三笔都从同一点出发，所以看起来是"扇翅的一瞬间"，不是两根不相干的线。
#
# 方案是拿 preview_butterfly.py 出的多轮预览挑的（C1）。

THORAX = (52.0, 68.0)                       # 胸部：两翅与圆点的共同起点
BODY_R = 6.5                                # 身体圆点半径

STROKES = (
    ((52, 68), (72, 46), (78, 20)),         # 短笔：前翅，偏竖
    ((52, 68), (98, 50), (108, 36)),        # 长笔：后翅，长而平
)
DOT = (THORAX[0], THORAX[1], BODY_R)        # 身体（实心圆）

# 标记的可见轮廓（含 7 单位线宽）要落在画布里的范围：
# 内缩 7.2% 的边距，同时按「最容易顶到边」的那一边定比例，保证四周留白均匀。
# 0.856 是照画布算的，圆角方块自己内缩了 PAD，所以相对底座的边距更大一点。
MARGIN = 0.072
# 视觉配重：几何居中后整个标记再微微左下挪一点（点偏右上，不挪会显得偏右上）
BIAS_X = -1.2
BIAS_Y = -1.2

# 逐尺寸生成参数：(边长, 额外超采样)。ss 越大越干净，代价是内存。
SIZES = ((1024, 4), (512, 4), (256, 4), (128, 4), (64, 4), (48, 4), (32, 8), (24, 8), (16, 16))


# ---------------------------------------------------------------- 曲线工具

def quad_point(p0, p1, p2, t):
    """二次贝塞尔在 t 处的点。"""
    u = 1.0 - t
    return (
        u * u * p0[0] + 2 * u * t * p1[0] + t * t * p2[0],
        u * u * p0[1] + 2 * u * t * p1[1] + t * t * p2[1],
    )


def flatten(ctrl, steps=None):
    """把二次贝塞尔采样成折线（点足够密，折线偏差 << 1/100 单位）。"""
    if steps is None:
        steps = FLATTEN_STEPS
    p0, p1, p2 = ctrl
    return np.array([quad_point(p0, p1, p2, i / steps) for i in range(steps + 1)])


def visible_bounds():
    """所有可见轮廓的边界（含线宽与圆端帽）。"""
    xs, ys = [], []
    r = STROKE_W / 2.0
    for ctrl in STROKES:
        pts = flatten(ctrl)
        xs += [pts[:, 0].min() - r, pts[:, 0].max() + r]
        ys += [pts[:, 1].min() - r, pts[:, 1].max() + r]
    return min(xs), min(ys), max(xs), max(ys)


def solve_placement():
    """求 scale 与 translate：可见轮廓等比缩放后落在内缩 MARGIN 的方框里。

    比例按「可见轮廓宽/高」里更吃紧的那个算，所以横竖边距一样。
    """
    x0, y0, x1, y1 = visible_bounds()
    vw, vh = x1 - x0, y1 - y0
    inner = VB * (1.0 - 2.0 * MARGIN)
    scale = round(min(inner / vw, inner / vh) * FIT, 4)

    # 先把可见轮廓对齐到居中的方框，再整体加视觉配重
    tx = (VB - scale * vw) / 2.0 - scale * x0 + BIAS_X
    ty = (VB - scale * vh) / 2.0 - scale * y0 + BIAS_Y

    return scale, round(tx, 4), round(ty, 4)


# ---------------------------------------------------------------- squircle

def squircle_path(a, cy_sign=1.0, cx_sign=1.0, samples=4096):
    """超椭圆参数曲线：x = a*sgn(cos t)|cos t|^(2/n)，y 同理。

    n>2 时曲线包住 (a,0)/(0,a)，所以缩放系数 1/cos(pi/n)^(2/n) 把它拉回边缘。
    """
    k = 1.0 / (math.cos(math.pi / SQUIRCLE_N) ** (2.0 / SQUIRCLE_N))
    pts = []
    for i in range(samples):
        t = 2.0 * math.pi * i / samples
        c, s = math.cos(t), math.sin(t)
        x = cx_sign * a * k * math.copysign(abs(c) ** (2.0 / SQUIRCLE_N), c)
        y = cy_sign * a * k * math.copysign(abs(s) ** (2.0 / SQUIRCLE_N), s)
        pts.append((VB / 2.0 + x, VB / 2.0 + y))
    return pts


def tile_alpha(size, ss):
    """在 size*ss 分辨率上画抗锯齿的圆角方块 alpha（0..1）。"""
    n = size * ss
    a = VB / 2.0 * (1.0 - PAD)
    pts = squircle_path(a)
    # 画布坐标 → 像素坐标
    f = n / VB
    poly = [(x * f, y * f) for x, y in pts]

    img = Image.new("L", (n, n), 0)
    ImageDraw.Draw(img).polygon(poly, fill=255)
    return np.asarray(img, dtype=np.float32) / 255.0


def glyph_mask(size, ss, scale, tx, ty):
    """在 size*ss 分辨率上画抗锯齿的标记 alpha（0..1）。

    走"到折线的精确距离场"而不是 ImageDraw.line：
    放大 4 倍后线宽有 200+ 像素，PIL 的折线扫描会在笔画里留下梳齿状条纹
    （同一段折线被反复描边、叠加），距离场没有这个问题，
    圆端帽 / 圆拐角也是数学上精确的。
    """
    n = size * ss
    f = n / VB

    def T(pt):
        return ((tx + scale * pt[0]) * f, (ty + scale * pt[1]) * f)

    # 折线点（图形坐标 → 像素），顺带求出标记的外接框
    segs = []
    lo = np.array([np.inf, np.inf], dtype=np.float32)
    hi = np.array([-np.inf, -np.inf], dtype=np.float32)
    for ctrl in STROKES:
        pts = np.array([T(p) for p in flatten(ctrl)], dtype=np.float32)
        segs.append(pts)
        lo = np.minimum(lo, pts.min(axis=0))
        hi = np.maximum(hi, pts.max(axis=0))
    hw = STROKE_W * scale * f / 2.0          # 线宽的一半（像素）

    # 只算标记附近这一块，画布其余部分必然是 0
    pad = hw + 2.0
    x_lo = int(max(0, math.floor(lo[0] - pad)))
    x_hi = int(min(n, math.ceil(hi[0] + pad)))
    y_lo = int(max(0, math.floor(lo[1] - pad)))
    y_hi = int(min(n, math.ceil(hi[1] + pad)))

    out = np.zeros((n, n), dtype=np.float32)
    if x_hi <= x_lo or y_hi <= y_lo:
        return out

    xs = np.arange(x_lo, x_hi, dtype=np.float32) + 0.5
    # 每条折线的 y 范围先算好，分带时用来筛掉够不着的那些
    spans = [(float(p[:, 1].min()), float(p[:, 1].max())) for p in segs]

    # 逐条横带算：一条带只跟附近那几段折线有关，
    # 整块画布一次算 640 段 × 500 万像素太慢，分带之后计算量差两个数量级。
    for y0 in range(y_lo, y_hi, CHUNK):
        y1 = min(y0 + CHUNK, y_hi)
        PX = xs[None, :]
        PY = (np.arange(y0, y1, dtype=np.float32) + 0.5)[:, None]
        py_min, py_max = float(PY.min()) - hw - 4.0, float(PY.max()) + hw + 4.0
        d = np.full((y1 - y0, x_hi - x_lo), np.inf, dtype=np.float32)
        for pts, (seg_lo, seg_hi) in zip(segs, spans):
            if seg_lo > py_max or seg_hi < py_min:
                continue
            # 再按段切小块：一条带一次算 160 段的距离会开出 (带高,带宽,段数)
            # 这种三维临时数组，几百 MB 起步；分块之后只有几 MB。
            for c0 in range(0, len(pts) - 1, SEG_CHUNK):
                sub = pts[c0:c0 + SEG_CHUNK + 1]
                ax, ay = sub[:-1, 0][None, None, :], sub[:-1, 1][None, None, :]
                bx, by = sub[1:, 0][None, None, :], sub[1:, 1][None, None, :]
                vx, vy = bx - ax, by - ay
                wx, wy = PX[..., None] - ax, PY[..., None] - ay
                L2 = vx * vx + vy * vy
                L2[L2 == 0] = 1.0
                # 最近点在段内的参数（别用 t：外面那个 t 是缩放平移量）
                s = np.clip((wx * vx + wy * vy) / L2, 0.0, 1.0)
                dx = wx - s * vx
                dy = wy - s * vy
                np.minimum(d, np.sqrt(dx * dx + dy * dy).min(axis=2), out=d)
        # 到折线的距离减掉线宽的一半，才是"到笔画边缘"的有符号距离
        d -= hw
        # 有符号距离 → 覆盖率：AA_WIDTH 像素的过渡带（smoothstep，比线性更干净）
        c = np.clip(0.5 - d / AA_WIDTH, 0.0, 1.0)
        out[y0:y1, x_lo:x_hi] = c * c * (3.0 - 2.0 * c)
    return out


# ---------------------------------------------------------------- 渲染

def render(size, extra_ss):
    """渲染一张 size×size 的 RGBA 图标。"""
    best = None
    # 优先 4 倍超采样；内存吃紧时自动降档，保证小尺寸也干净
    for ss in (extra_ss, 4, 2, 1):
        if ss < 1:
            continue
        try:
            best = _render_at(size, ss)
            break
        except MemoryError:
            continue
    if best is None:
        raise RuntimeError(f"{size}px 渲染失败：内存不足")
    return best


def _render_at(size, ss):
    scale, tx, ty = PLACEMENT
    ta = tile_alpha(size, ss)
    gm = glyph_mask(size, ss, scale, tx, ty)

    bg = np.zeros((size * ss, size * ss, 3), dtype=np.float32)
    bg[:] = np.array(BG, dtype=np.float32) / 255.0
    fg = np.zeros_like(bg)
    fg[:] = np.array(FG, dtype=np.float32) / 255.0

    top = gm[..., None] * fg + (1.0 - gm)[..., None] * bg
    out = np.concatenate([top, ta[..., None]], axis=2)

    img = Image.fromarray(np.clip(np.rint(out * 255.0), 0, 255).astype(np.uint8), "RGBA")
    if ss > 1:
        img = img.resize((size, size), Image.LANCZOS)
    return img


# ---------------------------------------------------------------- 输出

def write_svg(scale, tx, ty):
    a = VB / 2.0 * (1.0 - PAD)
    pts = squircle_path(a)
    poly = " ".join(f"{x:.3f},{y:.3f}" for x, y in pts)

    def num(v):
        return f"{v:g}"

    def path_d(ctrl):
        """二次贝塞尔 → SVG path（绝对坐标 M ... Q ...）。"""
        (x0, y0), (x1, y1), (x2, y2) = ctrl
        return f"M{num(x0)} {num(y0)}Q{num(x1)} {num(y1)} {num(x2)} {num(y2)}"

    strokes = "\n".join(f'    <path d="{path_d(c)}" />' for c in STROKES)

    svg = f"""<?xml version="1.0" encoding="UTF-8"?>
<!--
  Pi Agent Workspace 应用图标（母版 · viewBox {VB:g}x{VB:g}）
  ==================================================
  图形：一只用简单线条画的蝴蝶，来自启动动画
  （frontend/src/features/shell/StartupSplash.tsx 里那只蝴蝶 + 右上角的圆点）。

  启动动画的原始两条曲线是左右不对称的，缩到图标里更像一个抽象的 X，
  所以这里画了一版左右对称的蝴蝶：四片翅膀（上翅/下翅各一对，每片两笔勾轮廓）、
  一根竖直的身体、一对向上张开的触角。线条粗细/圆端帽/圆拐角跟启动动画一致。

  颜色取自 frontend/src/styles/tokens.css 的暗色主题：
  底座 --bg {BG[0]:02X}{BG[1]:02X}{BG[2]:02X}，蝴蝶 --text {FG[0]:02X}{FG[1]:02X}{FG[2]:02X}。

  圆角是 squircle（超椭圆 n={SQUIRCLE_N:g}），不是正圆角矩形；方块之外全透明。
  这个文件是「源」，所有 PNG/ICO/ICNS 都由它派生：
      python build/icons/render_icons.py
-->
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {VB:g} {VB:g}" width="{VB:g}" height="{VB:g}">
  <title>Pi Agent Workspace</title>

  <!-- 圆角底座：squircle，外面保持透明 -->
  <path fill="#{BG[0]:02X}{BG[1]:02X}{BG[2]:02X}" d="M{poly} Z" />

  <!-- 蝴蝶：四片翅膀 + 身体 + 触角，全部圆头笔画 -->
  <g transform="translate({num(tx)}, {num(ty)}) scale({num(scale)})"
     fill="none" stroke="#{FG[0]:02X}{FG[1]:02X}{FG[2]:02X}" stroke-width="{num(STROKE_W)}"
     stroke-linecap="round" stroke-linejoin="round">
{strokes}
  </g>
</svg>
"""
    p = ICONS / "pi-mark.svg"
    p.write_text(svg, encoding="utf-8")
    return p


# ---------------------------------------------------------------- 自检
def report(images):
    print("尺寸      bbox(不含透明)      不透明占比  角/心 alpha")
    for size in sorted(images, reverse=True):
        im = images[size]
        a = np.asarray(im)[..., 3]
        ys, xs = np.nonzero(a > 8)
        if len(xs) == 0:
            print(f"{size:>5}      <全透明!>")
            continue
        bbox = (xs.min(), ys.min(), xs.max(), ys.max())
        frac = float((a > 8).mean())
        corner = int(a[0, 0])
        center = int(a[size // 2, size // 2])
        print(f"{size:>5}      {str(bbox):<20} {frac:6.1%}      {corner}/{center}")


def main():
    global PLACEMENT

    ICONS.mkdir(parents=True, exist_ok=True)
    scale, tx, ty = solve_placement()
    PLACEMENT = (scale, tx, ty)

    x0, y0, x1, y1 = visible_bounds()
    print(f"标记可见范围 : x {x0:.2f}..{x1:.2f}  y {y0:.2f}..{y1:.2f}（画布 {VB:g}）")
    print(
        f"放置         : scale={scale}  translate=({tx}, {ty})  →  "
        f"落在 x {(tx + scale * x0):.2f}..{(tx + scale * x1):.2f}  "
        f"y {(ty + scale * y0):.2f}..{(ty + scale * y1):.2f}"
    )
    print(f"圆角         : squircle n={SQUIRCLE_N:g}\n")

    svg = write_svg(scale, tx, ty)
    images = {}
    for size, extra in SIZES:
        img = render(size, extra)
        images[size] = img
        img.save(ICONS / f"icon-{size}.png", optimize=True)

    # Wails 的图标源
    images[1024].save(BUILD / "appicon.png", optimize=True)

    print("生成：")
    print(f"  {svg.relative_to(ROOT)}")
    for size, _ in SIZES:
        p = ICONS / f"icon-{size}.png"
        print(f"  {p.relative_to(ROOT)}  ({p.stat().st_size:,} B)")
    p = BUILD / "appicon.png"
    print(f"  {p.relative_to(ROOT)}  ({p.stat().st_size:,} B)  ← Wails 图标源")
    print()
    print("下一步（.ico/.icns 由项目流水线从上面这张母版派生）：")
    print("  cd build && wails3 generate icons -input appicon.png \\")
    print("      -windowsfilename windows/icon.ico -macfilename darwin/icon.icns")

    print()
    report(images)


if __name__ == "__main__":
    main()
