#!/usr/bin/env python3
"""临时探针：把用户草图和某一版渲染按「横杠对齐」叠在一起，看腿的长度/弯度差多少。

    python build/icons/jellyfish/_match_probe.py <变体序号> <输出名>

草图的横杠端点是从 _sketch_probe.py 量出来的（笔直的连通域 #2）。
把草图的横杠端点映射到渲染图的横杠端点上（相似变换：缩放 + 旋转 + 平移），
再把整张草图按这个变换贴到渲染图上：
    红色 = 草图笔画   白色 = 我的渲染   黄色 = 重合
"""
from __future__ import annotations

import sys
from pathlib import Path

import numpy as np
from PIL import Image

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import render_jellyfish as R  # noqa: E402

SKETCH = Path(r"C:\Users\27801\.dsh\attachments\v1\objects\19"
              r"\197bd48cf3bacc584e88a7170ad460ccb815901978e4961bccd595a3af5960cb")
SK_BAR = np.array([[153.2, 54.7], [218.0, 88.9]])       # 草图横杠两端（像素）
SIZE = 720


def bbox_of(mask):
    ys, xs = np.nonzero(mask)
    return xs.min(), ys.min(), xs.max(), ys.max()


def main():
    idx = int(sys.argv[1]) if len(sys.argv) > 1 else 0
    tag = sys.argv[2] if len(sys.argv) > 2 else f"match{idx}"
    d = R.VARIANTS_3[idx]
    print(f"变体 {d.name}: 转 {d.rotate_deg:g}° 切 {d.slant_deg:g}° 腿型 {d.leg_style}")

    # 草图 → 二值
    sk = np.asarray(Image.open(SKETCH).convert("L"), dtype=np.float32) < 110
    sk[:4, :] = sk[-4:, :] = False
    sk[:, :4] = sk[:, -4:] = False

    # 我的渲染（无底座，纯图形）→ alpha
    img = R.render(SIZE, 2, d, "mark", dark=True)
    mine = np.asarray(img)[..., 3] > 128

    # 我的横杠端点在像素坐标
    scale, tx, ty = R.placement(d, "mark")
    m = R.transform_matrix(d)
    bar = R.apply_matrix(np.asarray(d.bar, dtype=np.float64), m)
    f = SIZE / R.VB
    me_bar = np.array([[(tx + scale * x) * f, (ty + scale * y) * f] for x, y in bar])

    # 相似变换：草图横杠 → 我的横杠（含镜像判断：这里不镜像，两边都是"左→右"）
    v_sk, v_me = SK_BAR[1] - SK_BAR[0], me_bar[1] - me_bar[0]
    s = np.linalg.norm(v_me) / np.linalg.norm(v_sk)
    th = np.arctan2(v_me[1], v_me[0]) - np.arctan2(v_sk[1], v_sk[0])
    co, si = np.cos(th), np.sin(th)
    Rm = np.array([[co, -si], [si, co]]) * s
    t = me_bar[0] - Rm @ SK_BAR[0]

    w = h = SIZE
    canvas = np.zeros((h, w), dtype=np.float32)                 # 0=空
    ys, xs = np.nonzero(sk)
    pts = np.stack([xs, ys], axis=1).astype(np.float64)
    mapped = pts @ Rm.T + t
    mi = np.rint(mapped).astype(int)
    keep = (mi[:, 0] >= 0) & (mi[:, 0] < w) & (mi[:, 1] >= 0) & (mi[:, 1] < h)
    sk_canvas = np.zeros((h, w), dtype=bool)
    sk_canvas[mi[keep, 1], mi[keep, 0]] = True

    # 把草图的笔画粗细按同一比例放大（不然线太细看不清）
    from PIL import ImageDraw
    thick = Image.new("L", (w, h), 0)
    dr = ImageDraw.Draw(thick)
    r = max(1, int(round(10 * s / 2)))     # 草图笔宽约 10px
    for y, x in zip(mi[keep, 1], mi[keep, 0]):
        dr.ellipse([x - r, y - r, x + r, y + r], fill=255)
    sk_canvas = np.asarray(thick) > 0

    rgb = np.zeros((h, w, 3), dtype=np.uint8)
    rgb[sk_canvas] = (255, 70, 70)
    rgb[mine] = (255, 255, 255)
    rgb[sk_canvas & mine] = (255, 235, 60)                      # 重合 = 黄

    out = HERE / f"{tag}.png"
    Image.fromarray(rgb).save(out)
    print(f"草图 bbox {bbox_of(sk)}  我的 bbox {bbox_of(mine)}")
    print(f"缩放 {s:.3f}  旋转 {np.degrees(th):.1f}°")
    print(f"重合率：草图笔画里 {float((sk_canvas & mine).sum()) / max(1, sk_canvas.sum()):.1%} 落在我这边；"
          f"我的笔画里 {float((sk_canvas & mine).sum()) / max(1, mine.sum()):.1%} 有草图覆盖")
    print("→", out)


if __name__ == "__main__":
    main()
