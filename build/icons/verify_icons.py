#!/usr/bin/env python3
"""校验图标产物。

    python build/icons/verify_icons.py

分工：build/appicon.png 由 render_icons.py 生成；
build/windows/icon.ico 与 build/darwin/icon.icns 由 `wails3 generate icons` 生成。
所以这里检查两件事：
  1) 母版 PNG：尺寸、RGBA、圆角处透明、中心不透明；
  2) .ico/.icns 的结构合法，且**每一帧都要和母版重采样出来的图对得上**——
     这一条最关键：它证明 wails3 派生出来的图标确实来自新的母版，
     而不是残留的旧图标或默认占位图。

预览图：preview-light.png / preview-dark.png。
"""

from __future__ import annotations

import struct
import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[2]
BUILD = ROOT / "build"
ICONS = BUILD / "icons"

OK = "  ok  "
BAD = " FAIL "

_CACHE = {}
failures = []


def check(cond, msg):
    print(f"[{OK if cond else BAD}] {msg}")
    if not cond:
        failures.append(msg)
    return cond


# ------------------------------------------------------------------ PNG

def master_small(size):
    """母版缩到指定尺寸（预览图缓存）。"""
    key = (size, "master")
    if key not in _CACHE:
        _CACHE[key] = Image.open(ICONS / "icon-1024.png").convert("RGBA").resize(
            (size, size), Image.LANCZOS)
    return _CACHE[key]


def matches_master(frame, size, tol=24):
    """把某帧跟"母版缩小到同尺寸"比一比，确认它确实来自新母版。

    不同缩放算法的边缘像素会有几个数的差异，所以用平均绝对差 + 非零占比判断，
    只要不是另一张图（旧图标 / 白色底 / 默认占位图）就一定能分开。
    """
    ref = np.asarray(master_small(size), dtype=np.int16)
    got = np.asarray(frame.convert("RGBA"), dtype=np.int16)
    if ref.shape != got.shape:
        return False
    diff = np.abs(ref - got)
    return float(diff.mean()) < tol and float((diff.max(axis=2) > 48).mean()) < 0.05


def corner_rule(size):
    """各尺寸下"四角够透明"的判定。

    圆角处一定是抗锯齿的过渡像素：16px 时一个像素只占画布 1.56%，
    而圆角半径有 3.5 个像素，所以角上那个像素本来就会落在曲线内侧。
    尺寸越大过渡占比越小，于是大尺寸直接要求 alpha==0。
    """
    if size >= 256:
        return 0, "alpha == 0"
    if size >= 48:
        return 2, "alpha <= 2"
    return 64, "alpha <= 64（小尺寸的圆角过渡像素）"


def check_pngs():
    print("── PNG ──")
    for size in (16, 24, 32, 48, 64, 128, 256, 512, 1024):
        p = ICONS / f"icon-{size}.png"
        if not check(p.exists(), f"{p.name} 存在"):
            continue
        im = Image.open(p)
        a = np.asarray(im.convert("RGBA"))[..., 3]
        check(im.size == (size, size), f"{p.name} 尺寸 {im.size}")
        check(im.mode == "RGBA", f"{p.name} 模式 {im.mode}")
        limit, why = corner_rule(size)
        corners = [int(a[0, 0]), int(a[0, -1]), int(a[-1, 0]), int(a[-1, -1])]
        check(max(corners) <= limit, f"{p.name} 四角透明 {corners}（{why}）")
        check(int(a[size // 2, size // 2]) == 255, f"{p.name} 中心不透明")
        # 圆角方块在四条边中点是贴边的（所以整块占满画布），
        # 真正该透明的地方是四个角——见上面 corners 的判定。
        check(int(a[0, size // 2]) == 255, f"{p.name} 上边中点贴边（alpha={int(a[0, size // 2])}）")

    # appicon.png 应与 1024 那份逐字节一致
    a = (BUILD / "appicon.png").read_bytes()
    b = (ICONS / "icon-1024.png").read_bytes()
    check(a == b, "build/appicon.png 与 icon-1024.png 一致")


# ------------------------------------------------------------------ ICO

def check_ico():
    print("── ICO（wails3 从 appicon.png 派生）──")
    p = BUILD / "windows" / "icon.ico"
    if not check(p.exists(), "windows/icon.ico 存在"):
        return
    raw = p.read_bytes()
    reserved, kind, count = struct.unpack_from("<HHH", raw, 0)
    check((reserved, kind) == (0, 1), f"文件头 reserved={reserved} type={kind}")

    seen = []
    for i in range(count):
        w, h, colors, res, planes, bpp, nbytes, off = struct.unpack_from("<BBBBHHII", raw, 6 + 16 * i)
        w = w or 256
        h = h or 256
        seen.append(w)
        blob = raw[off:off + nbytes]
        is_png = blob[:8] == b"\x89PNG\r\n\x1a\n"
        check(off + nbytes <= len(raw) and is_png,
              f"条目 {w}×{h}: 偏移 {off} 长度 {nbytes} bpp={bpp} PNG={is_png}")
        if not is_png:
            continue
        import io

        im = Image.open(io.BytesIO(blob)).convert("RGBA")
        arr = np.asarray(im)[..., 3]
        limit, why = corner_rule(w)
        ca = int(arr[0, 0])
        check(im.size == (w, h), f"  帧 {w} 解出尺寸 {im.size}")
        check(ca <= limit, f"  帧 {w} 左上角透明（alpha={ca}，{why}）")
        check(int(arr[w // 2, h // 2]) == 255, f"  帧 {w} 中心不透明")
        check(matches_master(im, w), f"  帧 {w} 与母版一致（不是旧图标/占位图）")

    # Windows 想要 16/32/48/256 这套；24 有更好但不是必须
    for need in (16, 32, 48, 256):
        check(need in seen, f"包含 {need}px 帧")
    check(seen == sorted(seen, reverse=True), f"从大到小排列 {seen}")


# ------------------------------------------------------------------ ICNS

ICNS_KNOWN = {
    b"icp4": 16, b"icp5": 32, b"icp6": 64,
    b"ic07": 128, b"ic08": 256, b"ic09": 512, b"ic10": 1024,
    b"ic11": 32, b"ic12": 64, b"ic13": 256, b"ic14": 512,
}


def check_icns():
    print("── ICNS（wails3 从 appicon.png 派生）──")
    p = BUILD / "darwin" / "icon.icns"
    if not check(p.exists(), "darwin/icon.icns 存在"):
        return
    raw = p.read_bytes()
    check(raw[:4] == b"icns", f"magic {raw[:4]!r}")
    (total,) = struct.unpack_from(">I", raw, 4)
    check(total == len(raw), f"头部长度 {total} == 文件长度 {len(raw)}")

    pos, n, sizes = 8, 0, []
    while pos < len(raw):
        code = raw[pos:pos + 4]
        (ln,) = struct.unpack_from(">I", raw, pos + 4)
        body = raw[pos + 8:pos + ln]
        is_png = body[:8] == b"\x89PNG\r\n\x1a\n"
        check(ln >= 8 and pos + ln <= len(raw) and is_png,
              f"条目 {code.decode('latin1')} 长度 {ln} PNG={is_png}")
        if code in ICNS_KNOWN and is_png:
            import io

            im = Image.open(io.BytesIO(body)).convert("RGBA")
            size = im.size[0]
            sizes.append(size)
            check(size == ICNS_KNOWN[code],
                  f"  {code.decode('latin1')} → {size}px（期望 {ICNS_KNOWN[code]}）")
            check(matches_master(im, size), f"  {code.decode('latin1')} 与母版一致")
        n += 1
        pos += ln
    check(pos == len(raw), "条目正好铺满文件，没有多余字节")
    for need in (128, 256, 512, 1024):
        check(need in sizes, f"包含 {need}px 条目")


# ------------------------------------------------------------------ 预览图

def contact_sheet(dark: bool, scale: int = 2):
    """两行：第一行各真实尺寸（按底边对齐），第二行 256 原大 / 放大 2x / 贴彩色块。"""
    sizes = [16, 24, 32, 48, 64, 128, 256]
    pad, gap = 32, 26
    zoom = 256 * scale
    label_h = 26
    col_w = (zoom, zoom, 256)
    place = []                       # 第二行每格的 (x, y, 绘制尺寸)
    x = pad
    for w in col_w:
        place.append((x, pad + 296 + (zoom - w), w))
        x += w + gap

    W = pad * 2 + sum(col_w) + gap * (len(col_w) - 1)
    H = pad + 296 + zoom + label_h + pad
    bg = (24, 24, 27) if dark else (244, 244, 245)
    ink = (200, 200, 206) if dark else (60, 60, 66)

    canvas = Image.new("RGB", (W, H), bg)
    d = ImageDraw.Draw(canvas)

    # 第一行：真实尺寸，底边对齐到 y=pad+256
    x = pad
    base = pad + 256
    for s in sizes:
        im = Image.open(ICONS / f"icon-{s}.png").convert("RGBA")
        canvas.paste(im, (x, base - s), im)
        d.text((x + max(0, s // 2 - 6), base + 10), str(s), fill=ink)
        x += s + gap

    big = Image.open(ICONS / "icon-256.png").convert("RGBA")

    # 第二行左：256 原大
    bx, by, _ = place[0]
    canvas.paste(big, (bx, by), big)
    d.text((bx, by + 256 + 6), "256 原大", fill=ink)

    # 第二行中：放大 2x（NEAREST，看圆角过渡与边缘锯齿）
    zx, zy, _ = place[1]
    up = big.resize((zoom, zoom), Image.NEAREST)
    canvas.paste(up, (zx, zy), up)
    d.text((zx, zy + zoom + 6), f"放大 {scale}x · 看圆角", fill=ink)

    # 第二行右：贴到纯色块上（圆角外应露出底色 = 真透明）
    sx, sy, _ = place[2]
    swatch = Image.new("RGB", (256, 256), (255, 90, 90) if dark else (0, 122, 255))
    swatch.paste(big, (0, 0), big)
    canvas.paste(swatch, (sx, sy))
    d.text((sx, sy + 256 + 6), "贴色块 · 圆角外露底色", fill=ink)

    out = ICONS / ("preview-dark.png" if dark else "preview-light.png")
    canvas.save(out)
    return out


def main():
    check_pngs()
    check_ico()
    check_icns()
    print("── 预览图 ──")
    for dark in (False, True):
        out = contact_sheet(dark)
        print(f"[{OK}] {out.relative_to(ROOT)}")

    print()
    if failures:
        print(f"❌ {len(failures)} 项不通过：")
        for f in failures:
            print("   -", f)
        sys.exit(1)
    print("✅ 全部通过")


if __name__ == "__main__":
    main()
