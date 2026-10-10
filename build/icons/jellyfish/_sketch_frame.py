#!/usr/bin/env python3
"""临时探针：把草图的笔画"摆正"到图形坐标系里，看两条腿真实的形状与长度。

草图横杠端点 → 图形坐标系的 (16,24)→(84,24)，于是所有笔画都落在同一个
参照系里，可以照着它调贝塞尔控制点。用完即删。
"""
from __future__ import annotations

from collections import deque
from pathlib import Path

import numpy as np
from PIL import Image

SKETCH = Path(r"C:\Users\27801\.dsh\attachments\v1\objects\19"
              r"\197bd48cf3bacc584e88a7170ad460ccb815901978e4961bccd595a3af5960cb")
SK_BAR = np.array([[153.2, 54.7], [218.0, 88.9]])
ART_BAR = np.array([[16.0, 24.0], [84.0, 24.0]])

im = Image.open(SKETCH).convert("L")
mask = np.asarray(im, dtype=np.float32) < 110
mask[:4, :] = mask[-4:, :] = False
mask[:, :4] = mask[:, -4:] = False
H, W = mask.shape

labels = np.zeros((H, W), dtype=np.int32)
comps, cur = [], 0
for y0 in range(H):
    for x0 in range(W):
        if not mask[y0, x0] or labels[y0, x0]:
            continue
        cur += 1
        q = deque([(y0, x0)])
        labels[y0, x0] = cur
        pix = []
        while q:
            y, x = q.popleft()
            pix.append((x, y))
            for dy, dx in ((1, 0), (-1, 0), (0, 1), (0, -1)):
                ny, nx = y + dy, x + dx
                if 0 <= ny < H and 0 <= nx < W and mask[ny, nx] and not labels[ny, nx]:
                    labels[ny, nx] = cur
                    q.append((ny, nx))
        comps.append(np.array(pix, dtype=np.float64))
comps = [c for c in sorted(comps, key=len, reverse=True) if len(c) >= 20]

# 相似变换：草图横杠 → 图形坐标系的横杠
v_sk, v_art = SK_BAR[1] - SK_BAR[0], ART_BAR[1] - ART_BAR[0]
s = np.linalg.norm(v_art) / np.linalg.norm(v_sk)
th = np.arctan2(v_art[1], v_art[0]) - np.arctan2(v_sk[1], v_sk[0])
co, si = np.cos(th), np.sin(th)
M = np.array([[co, -si], [si, co]]) * s
t = ART_BAR[0] - M @ SK_BAR[0]

print(f"相似变换：缩放 {s:.4f}  旋转 {np.degrees(th):+.1f}°")
print("（图形坐标系：横杠 (16,24)→(84,24)，y 向下）\n")

for idx, p in enumerate(comps):
    c = p.mean(axis=0)
    _, _, vt = np.linalg.svd(p - c, full_matrices=False)
    ax = vt[0]
    proj = (p - c) @ ax
    bins = 18
    lo, hi = proj.min(), proj.max()
    pts = []
    for k in range(bins):
        b0 = lo + k * (hi - lo) / bins
        b1 = lo + (k + 1) * (hi - lo) / bins
        sel = p[(proj >= b0) & (proj <= b1)]
        if len(sel):
            pts.append(sel.mean(axis=0))
    pts = np.array(pts)
    art = pts @ M.T + t
    if art[0, 0] > art[-1, 0]:
        art = art[::-1]
    kind = "横杠" if abs(np.degrees(np.arctan2(*(art[-1] - art[0])[::-1]))) < 6 else "腿"
    seg = np.linalg.norm(np.diff(art, axis=0), axis=1).sum()
    print(f"#{idx} {kind}  弧长 {seg:6.1f}（图形单位）  "
          f"端点 ({art[0][0]:6.1f},{art[0][1]:6.1f})→({art[-1][0]:6.1f},{art[-1][1]:6.1f})")
    print("    中心线: " + " ".join(f"({x:.0f},{y:.0f})" for x, y in art))
    if kind == "腿":
        d0 = art[1] - art[0]
        d1 = art[-1] - art[-2]
        a0 = np.degrees(np.arctan2(d0[1], d0[0])) % 360
        a1 = np.degrees(np.arctan2(d1[1], d1[0])) % 360
        print(f"    起笔方向 {a0:5.1f}°（180=向左, 90=向下）  收笔方向 {a1:5.1f}°")
    print()
