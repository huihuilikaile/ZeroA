#!/usr/bin/env python3
"""临时探针：从用户手绘草图里量出「倾斜」的实际角度与笔画走向。用完即删。"""
from __future__ import annotations

import sys
from collections import deque

import numpy as np
from PIL import Image

SRC = sys.argv[1]
im = Image.open(SRC).convert("L")
a = np.asarray(im, dtype=np.float32)
print("size", im.size, "灰度范围", a.min(), a.max())

mask = a < 110
H, W = mask.shape

# 去掉贴边的细框线/噪点
mask[:4, :] = False
mask[-4:, :] = False
mask[:, :4] = False
mask[:, -4:] = False

# 连通域（4 邻域 BFS）
labels = np.zeros((H, W), dtype=np.int32)
cur = 0
comps = []
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

comps.sort(key=len, reverse=True)
print(f"连通域 {len(comps)} 个，>20px 的：")
for i, p in enumerate(comps):
    if len(p) < 20:
        continue
    c = p.mean(axis=0)
    # PCA 主轴
    u, s, vt = np.linalg.svd(p - c, full_matrices=False)
    ax = vt[0]
    ang = np.degrees(np.arctan2(ax[1], ax[0])) % 180.0
    t = (p - c) @ ax
    perp = (p - c) @ vt[1]
    e1 = c + ax * t.min()
    e2 = c + ax * t.max()
    # 厚度（沿法向的展开，用于估计笔宽）
    print(f"  #{i} 像素 {len(p):5d}  中心 ({c[0]:6.1f},{c[1]:6.1f})  轴 {ang:6.1f}°  "
          f"长 {t.max()-t.min():6.1f}  厚 {perp.max()-perp.min():5.1f}  "
          f"端点 ({e1[0]:6.1f},{e1[1]:6.1f})→({e2[0]:6.1f},{e2[1]:6.1f})")
    # 沿主轴的走向采样：把点按 t 分 8 段，看每段的中心（能看出弯折方向）
    n = 8
    seg = []
    for k in range(n):
        lo, hi = t.min() + k * (t.max() - t.min()) / n, t.min() + (k + 1) * (t.max() - t.min()) / n
        sel = p[(t >= lo) & (t <= hi)]
        if len(sel):
            seg.append(sel.mean(axis=0))
    print("      走向:", " ".join(f"({p[0]:.0f},{p[1]:.0f})" for p in seg))

ys, xs = np.nonzero(mask)
print(f"整体 bbox x {xs.min()}..{xs.max()}  y {ys.min()}..{ys.max()}  "
      f"({xs.max()-xs.min()}×{ys.max()-ys.min()})")
allp = np.stack([xs, ys], axis=1).astype(np.float64)
c = allp.mean(axis=0)
u, s, vt = np.linalg.svd(allp - c, full_matrices=False)
print(f"整体主轴 {np.degrees(np.arctan2(vt[0][1], vt[0][0])) % 180:.1f}°  奇异值比 {s[0]/s[1]:.2f}")
