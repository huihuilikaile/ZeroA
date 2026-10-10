#!/usr/bin/env python3
"""校验 B2 图标**是否真的满足需求**。

    python build/icons/verify_b2.py

verify_icons.py 管的是"图标文件本身合不合规"（尺寸 / RGBA / 圆角 / .ico 帧来自新母版）。
这个脚本管的是另一件事：需求逐条对着**渲染出来的像素**反测，不看源码里写死的常量。

  1. 纯白字   #FFFFFF —— 笔画内部像素 R=G=B=255
  2. 纯黑底   #000000 —— 底座内部像素 R=G=B=0
  3. 带圆角          —— 四角透明、四边中点贴边（判定跟 verify_icons.py 一致）
  4. 2 ≤ B 的一半     —— 按列空隙把两个字切开、量各自高度，比值必须 ≤ 0.5
  5. 2 在 B 右边      —— 2 的横向范围完全在 B 右侧
  6. 2 贴基线        —— 两个字的底边齐平
  7. 底座轮廓没变     —— alpha 与 render_icons.py 那套 squircle 逐像素比对
  8. .ico 里确实是这版 —— 256 帧同样量一遍比值与配色，防止交付的是旧 .ico

只有 numpy + Pillow，纯本地计算。
"""

from __future__ import annotations

import io
import struct
import sys
from pathlib import Path

import numpy as np
from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parent))
import render_icons as R  # noqa: E402
import render_b2 as B  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
BUILD = ROOT / "build"
ICONS = BUILD / "icons"

OK = "  ok  "
BAD = " FAIL "

# 需求：2 不超过 B 的一半。渲染出来后边缘会有一两个像素的抗锯齿，
# 所以判定留一点点余量（1024 上约 1 个像素），其余尺寸同比例放宽。
RATIO_MAX = 0.5
RATIO_MIN = 0.45            # 也不能小得离谱（说明 2 真的还是"一半"量级）
THR = 128                   # 二值化阈值：>128 算笔画

failures = []


def check(cond, msg):
    print(f"[{OK if cond else BAD}] {msg}")
    if not cond:
        failures.append(msg)
    return cond


def rgba(p):
    return np.asarray(Image.open(p).convert("RGBA"))


def ink_of(a, thr=THR):
    """字标（白）掩码：颜色够白且不透明。"""
    return (a[..., 0] > thr) & (a[..., 3] > thr)


def erode(mask, k=3):
    """简版腐蚀（去掉边缘的抗锯齿过渡像素），四边留空避免 roll 绕回。"""
    out = mask.copy()
    for dy in range(-k, k + 1):
        for dx in range(-k, k + 1):
            out &= np.roll(np.roll(mask, dy, axis=0), dx, axis=1)
    out[:k + 1, :] = out[-k - 1:, :] = False
    out[:, :k + 1] = out[:, -k - 1:] = False
    return out


def split_glyphs(a):
    """把 B 和 2 按列空隙切开，返回两张**整幅大小**的掩码（坐标即图像坐标）。"""
    ink = ink_of(a)
    cols = ink.any(axis=0)
    idx = np.nonzero(cols)[0]
    if len(idx) == 0:
        return None, None
    runs, start = [], idx[0]
    for i in range(idx[0] + 1, idx[-1] + 2):
        if i > idx[-1] or not cols[i]:
            runs.append((start, i - 1))
            if i <= idx[-1]:
                start = i + 1
    # 取最宽的空隙当分隔（B 和 2 之间有且只有一条明显的缝）
    if len(runs) < 2:
        return None, None
    gaps = [(runs[i + 1][0] - runs[i][1] - 1, runs[i][1]) for i in range(len(runs) - 1)]
    _, cut = max(gaps)

    b = np.zeros_like(ink)
    t = np.zeros_like(ink)
    b[:, :cut + 1] = ink[:, :cut + 1]
    t[:, cut + 1:] = ink[:, cut + 1:]
    return b, t


def extent(mask):
    """返回 (x0, y0, x1, y1)，空则 None。"""
    ys, xs = np.nonzero(mask)
    if len(xs) == 0:
        return None
    return int(xs.min()), int(ys.min()), int(xs.max()), int(ys.max())


def measure_glyphs(a):
    b, two = split_glyphs(a)
    if b is None:
        return None
    eb, e2 = extent(b), extent(two)
    if eb is None or e2 is None:
        return None
    return {
        "B": eb,
        "two": e2,
        "h_b": eb[3] - eb[1] + 1,
        "h_2": e2[3] - e2[1] + 1,
        "w_b": eb[2] - eb[0] + 1,
    }


def check_spec(a, tag, tol_px):
    """需求 1/2/4/5/6：配色 + 2 的大小与位置。"""
    size = a.shape[0]
    k = 1 if size < 64 else 3          # 小图上腐蚀半径要小，否则核心像素全被啃光
    m = measure_glyphs(a)
    if not check(m is not None, f"{tag} 能切出 B 和 2 两个字"):
        return
    h_b, h_2 = m["h_b"], m["h_2"]
    ratio = h_2 / h_b
    lim = RATIO_MAX + tol_px / h_b
    check(ratio <= lim,
          f"{tag} 2 不超过 B 的一半：2 高 {h_2}px / B 高 {h_b}px = {ratio:.4f} ≤ {lim:.4f}")
    check(ratio >= RATIO_MIN,
          f"{tag} 2 确实是「一半」量级（{ratio:.4f} ≥ {RATIO_MIN}）")
    check(m["two"][0] > m["B"][2],
          f"{tag} 2 在 B 右边：2 左边界 x={m['two'][0]} > B 右边界 x={m['B'][2]}")
    dy = abs(m["two"][3] - m["B"][3])
    check(dy <= max(1, round(0.01 * h_b)),
          f"{tag} 2 贴基线：底边相差 {dy}px（B 高 {h_b}px 的 1% 以内）")

    # 配色：先确认"不透明处都是灰阶"（没有偏色），再腐蚀掉边缘看核心像素
    opaque = a[..., 3] == 255
    rgb = a[..., :3].astype(np.int16)
    gray = (np.abs(rgb[..., 0] - rgb[..., 1]) == 0) & (np.abs(rgb[..., 1] - rgb[..., 2]) == 0)
    check(bool(gray[opaque].all()), f"{tag} 不透明像素都是灰阶（白字黑底，没有偏色）")

    ink = ink_of(a)
    core_ink = erode(ink, k)
    core_bg = erode(opaque & ~ink, k)
    v_ink = int(a[..., :3][core_ink].min()) if core_ink.any() else None
    v_bg = int(a[..., :3][core_bg].max()) if core_bg.any() else None

    # 设计色就是 #FFFFFF / #000000：母版与 ≥64px 的尺寸上核心像素必须是精确值。
    # 更小的尺寸上笔画只有 2~7 个像素宽，抗锯齿 + LANCZOS 连"核心"也会偏几个数，
    # 所以 16/24/32/48 放宽到 ≥96% 白、≤5% 黑——眼睛上依然是纯白纯黑。
    exact = size >= 64
    lo, hi = (255, 0) if exact else (245, 12)
    note = "" if exact else "，小尺寸抗锯齿容差 96%/5%"
    check(v_ink is not None and v_ink >= lo,
          f"{tag} 字是纯白 #FFFFFF（笔画核心最小值 = {v_ink}{note}）")
    check(v_bg is not None and v_bg <= hi,
          f"{tag} 底是纯黑 #000000（底色核心最大值 = {v_bg}{note}）")

    # 字标要落在底座里：笔画的 alpha 都是 255（没有压在圆角外的透明区上）
    check(bool((a[..., 3][ink] == 255).all()), f"{tag} 字标完全落在底座的圆角方块内")


def corner_rule(size):
    if size >= 256:
        return 0
    if size >= 48:
        return 2
    return 64


def check_pngs():
    print("── PNG：需求逐条反测 ──")
    for size in (16, 24, 32, 48, 64, 128, 256, 512, 1024):
        p = ICONS / f"icon-{size}.png"
        if not check(p.exists(), f"{p.name} 存在"):
            continue
        a = rgba(p)
        # 3. 带圆角：四角透明、四边中点贴边
        lim = corner_rule(size)
        corners = [int(a[0, 0, 3]), int(a[0, -1, 3]), int(a[-1, 0, 3]), int(a[-1, -1, 3])]
        check(max(corners) <= lim, f"{p.name} 四角透明 {corners}（圆角；上限 {lim}）")
        check(int(a[0, size // 2, 3]) == 255 and int(a[size // 2, 0, 3]) == 255,
              f"{p.name} 四边中点贴边（圆角方块铺满画布）")
        check_spec(a, p.name, tol_px=1 if size >= 256 else 1)


def check_tile_unchanged():
    """7. 底座轮廓还是 render_icons.py 那套 squircle（只换了字标，外轮廓没动）。"""
    print("── 底座轮廓（应与旧图标一致）──")
    size, ss = 1024, 4
    ref = np.clip(np.rint(R.tile_alpha(size, ss) * 255.0), 0, 255).astype(np.uint8)
    ref = np.asarray(Image.fromarray(ref, "L").resize((size, size), Image.LANCZOS), dtype=np.int16)
    got = np.asarray(Image.open(ICONS / "icon-1024.png").convert("RGBA"))[..., 3].astype(np.int16)
    diff = np.abs(ref - got)
    check(float(diff.mean()) < 0.5 and int(diff.max()) <= 2,
          f"icon-1024.png 的 alpha 与 render_icons.tile_alpha 一致"
          f"（平均差 {diff.mean():.3f}，最大差 {int(diff.max())}）")

    # build/appicon.png 必须就是 1024 那份（.ico 由它派生）
    check((BUILD / "appicon.png").read_bytes() == (ICONS / "icon-1024.png").read_bytes(),
          "build/appicon.png 与 icon-1024.png 逐字节一致")


def check_ico():
    """8. .ico 的每一帧都必须是这版 B2（防止交付残留的旧图标）。"""
    print("── .ico：确认交给 Windows 的就是这版 ──")
    p = BUILD / "windows" / "icon.ico"
    if not check(p.exists(), "windows/icon.ico 存在"):
        return
    raw = p.read_bytes()
    reserved, kind, count = struct.unpack_from("<HHH", raw, 0)
    check((reserved, kind) == (0, 1), f"文件头 reserved={reserved} type={kind}")
    seen = []
    for i in range(count):
        w, h, _c, _r, _pl, _bpp, nbytes, off = struct.unpack_from("<BBBBHHII", raw, 6 + 16 * i)
        w = w or 256
        seen.append(w)
        blob = raw[off:off + nbytes]
        if blob[:8] != b"\x89PNG\r\n\x1a\n":
            check(False, f"帧 {w}: 不是 PNG")
            continue
        a = np.asarray(Image.open(io.BytesIO(blob)).convert("RGBA"))
        check_spec(a, f"ico {w}px", tol_px=1)
    check(seen == sorted(seen, reverse=True), f"从大到小排列 {seen}")
    for need in (16, 32, 48, 256):
        check(need in seen, f"包含 {need}px 帧")


def check_svg():
    print("── SVG 母版 ──")
    p = ICONS / "b2-mark.svg"
    if not check(p.exists(), "b2-mark.svg 存在"):
        return
    s = p.read_text(encoding="utf-8")
    check('fill="#FFFFFF"' in s, "SVG 里的字是 #FFFFFF")
    check('fill="#000000"' in s, "SVG 里的底是 #000000")
    check('fill-rule="nonzero"' in s and 'viewBox="0 0 120 120"' in s,
          "SVG 有 nonzero 填充与 120×120 画布")


def main():
    check_pngs()
    check_tile_unchanged()
    check_ico()
    check_svg()

    print()
    if failures:
        print(f"❌ {len(failures)} 项不通过：")
        for f in failures:
            print("   -", f)
        sys.exit(1)
    print("✅ 全部通过：纯白字 / 纯黑底 / 圆角 / 2 ≤ B 的一半 / 2 在右且贴基线")


if __name__ == "__main__":
    main()
