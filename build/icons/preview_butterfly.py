#!/usr/bin/env python3
"""蝴蝶图标候选预览。

    python build/icons/preview_butterfly.py

把几个"简单线条画蝴蝶"的方案各渲染一张 256 + 64 的图，拼成预览，
用来挑方案。每个方案都复用 render_icons.py 那套渲染器
（圆角底座 + 距离场笔画），所以预览所见即最终图标。
"""

from __future__ import annotations

import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

sys.path.insert(0, str(Path(__file__).resolve().parent))
import render_icons as R  # noqa: E402

ICONS = Path(__file__).resolve().parent
CENTER = R.VB / 2.0


# ---------------------------------------------------------------- 形状工具

def q(p0, p1, p2):
    return (p0, p1, p2)


def mirror(ctrl, axis=None):
    """沿竖直中轴镜像一条二次贝塞尔。"""
    if axis is None:
        axis = CENTER
    (ax, ay), (bx, by), (cx, cy) = ctrl
    return ((2 * axis - ax, ay), (2 * axis - bx, by), (2 * axis - cx, cy))


def both_sides(strokes, axis=None):
    """每条曲线都补一条镜像的，得到左右对称的图形。"""
    out = list(strokes)
    out += [mirror(s, axis) for s in strokes]
    return out


# ---------------------------------------------------------------- 候选方案
#
# 全部在 120x120 的画布坐标里画；外面再统一缩放居中（跟主脚本一样）。
#
# A 参考：现在这版 —— 启动动画那两条曲线 + 右上角的点
A_STROKES = [q((12, 46), (30, 50), (58, 58)), q((24, 98), (60, 34), (78, 36))]
A_DOTS = [(96, 26, 9)]

# B 两片翅膀：保留"两条曲线"的味道，但左右对称 + 一对触角
B_STROKES = both_sides([
    q((52, 30), (18, 30), (14, 62)),      # 翅膀上缘：从身体附近甩出去
    q((14, 62), (26, 84), (54, 78)),      # 翅膀下缘：收回来
    q((58, 34), (50, 22), (40, 12)),      # 触角
])
B_DOTS = [(52, 40, 5.5), (68, 40, 5.5)]

# C 四片翅膀：上翅 + 下翅各一对，加触角（100 单位内，后面整体放大到 96）
C_STROKES = both_sides([
    q((54, 46), (10, 16), (8, 45)),       # 上翅：往外甩，翅尖在左上
    q((8, 45), (20, 66), (52, 58)),       # 上翅：收回身体
    q((54, 62), (14, 60), (16, 84)),      # 下翅：往外荡
    q((16, 84), (28, 98), (54, 78)),      # 下翅：收回身体
    q((56, 32), (48, 20), (38, 10)),      # 触角
], axis=50.0)
C_STROKE_W = 5.0
C_SCALE_W = 1.143                         # 100 单位画布 → 96 单位

# D 四片翅膀 · 粗线圆润：翅形更圆，笔画更粗，缩到 16px 也立得住
D_STROKES = both_sides([
    q((54, 48), (14, 20), (12, 50)),      # 上翅
    q((12, 50), (24, 70), (52, 60)),
    q((54, 64), (18, 64), (22, 88)),      # 下翅
    q((22, 88), (34, 100), (54, 80)),
    q((57, 34), (50, 22), (42, 14)),      # 触角
], axis=57.0)
D_STROKE_W = 6.0
D_SCALE_W = 0.96

# E 极简：只留"翅膀 + 圆点"，不加触角，最接近现在这版的克制程度
E_STROKES = both_sides([
    q((52, 34), (14, 22), (13, 56)),
    q((13, 56), (26, 78), (53, 70)),
    q((53, 70), (40, 82), (34, 96)),      # 后翅当一条小尾巴
])
E_DOTS = [(52, 46, 7.0)]

# F 四片翅膀 · 克制版：身体是竖直中线，触角向上张开，翅形收窄一点
F_BODY = q((60, 29), (60.5, 62), (60, 95))    # 身体：几乎竖直的一条线
F_STROKES = both_sides([
    q((57, 42), (18, 16), (16, 44)),      # 上翅
    q((16, 44), (26, 60), (57, 54)),
    q((57, 62), (22, 60), (25, 84)),      # 下翅
    q((25, 84), (36, 96), (57, 74)),
    q((57, 33), (46, 20), (38, 11)),      # 触角：从头部往外上方张
], axis=CENTER)

# —— 侧飞系列：蝴蝶从侧面看，一长一短两笔 ——
#
# 朝向是往右上方飞：短笔 = 前翅（指向前上），长笔 = 后翅（甩向后上），
# 两笔交在身体处。这才对得上"侧边飞、简单的一长一短线条"。
S_STROKES = [
    q((50, 66), (96, 46), (100, 30)),     # 长笔：后翅
    q((50, 66), (40, 40), (52, 14)),      # 短笔：前翅
]
S_DOTS = [(52, 66, 6.0)]

# T = S 去掉身体圆点，真的只剩一长一短两笔
S2_STROKES = [
    q((50, 66), (98, 44), (102, 26)),
    q((50, 66), (42, 42), (54, 16)),
]
S2_DOTS = [(51, 67, 5.0)]

# U 长短对比更强：后翅更长更平，前翅更短，飞行感更明显
U_STROKES = [
    q((48, 70), (100, 52), (104, 28)),
    q((48, 70), (42, 50), (56, 30)),
]
U_DOTS = [(50, 70, 5.5)]

# V 线条更弯：翅尖上翘，像扇翅的那一瞬间
V_STROKES = [
    q((46, 72), (80, 62), (100, 26)),
    q((46, 72), (36, 48), (56, 18)),
]
V_DOTS = [(48, 72, 5.5)]

# W 两笔都上翘、长短分明，翅膀张得更开
W_STROKES = [
    q((44, 74), (86, 58), (104, 22)),
    q((44, 74), (34, 46), (50, 12)),
]
W_DOTS = [(46, 74, 5.5)]

# —— X 系列：真正的侧飞姿势 ——
#
# 关键区别：两笔翅膀都从**胸部**往前上方 / 后上方张开，头在更前面一点，
# 身体往后下方拖着。所以轮廓是"翅膀戳向前上 + 拖一条后缘"，
# 而不是两条线交成一个 V（V 读起来像对勾）。
X_THORAX = (50, 62)
X_BODY = q((50, 62), (33, 73), (18, 82))       # 身体：一条向后下方拖的线

# X1 身体 + 前翅（短）+ 后翅（长），不点头
X1_STROKES = [
    X_BODY,
    q(X_THORAX, (66, 40), (74, 16)),            # 短笔：前翅，指向前上
    q(X_THORAX, (92, 54), (106, 44)),           # 长笔：后翅，甩向后上
]

# X2 = X1 + 头点（点在身体前端，给个"活的"朝向）
X2_DOTS = [(53, 59, 5.0)]

# X3 = X1 + 一根触角（从头部往前上方挑一下）
X3_STROKES = X1_STROKES + [q((53, 60), (68, 52), (76, 42))]

# Y 两笔版：只有前翅 + 后翅，共用胸部为起点，靠那一笔后缘读成侧飞
Y_STROKES = [
    q((48, 64), (70, 42), (78, 18)),            # 短笔：前翅
    q((48, 64), (86, 52), (100, 40)),           # 长笔：后翅
]
Y_DOTS = [(48, 64, 5.5)]

# Z 两笔 + 身体线（不点头）：身体把"头在前、尾在后"交代清楚
Z_STROKES = [
    q((50, 62), (70, 42), (78, 16)),            # 短笔：前翅
    q((50, 62), (88, 52), (104, 42)),           # 长笔：后翅
    q((50, 62), (34, 72), (22, 82)),            # 身体
]

def flip_x(strokes, axis=None):
    """整组笔画左右翻转（借着 mirror 用）。"""
    if axis is None:
        axis = CENTER
    return [mirror(s, axis) for s in strokes]


# —— B 系列：在飞的蝴蝶侧影 ——
#
# 上一批（Z / X1 / Y）只画了"两条线交一点"，读出来像树枝或对勾，不像蝴蝶。
# 侧面剪影要立得住，缺的是两样东西：
#   1) 朝前上方挑起的**触角**（一只昆虫的头部特征）
#   2) 往后下方甩出去的**后翅后缘**（蝴蝶最显眼的那条斜线）
# 所以这版是：头在左上、身体斜着往后下拖、两片翅膀从胸部往右扇开、触角往前挑。
# 默认朝左飞；*_R 是同一形状左右翻转（朝右飞）的版本。

FLY_HEAD = (56, 46)                            # 头
FLY_THORAX = (72, 60)                          # 胸部（翅膀的起点）
FLY_BODY = q((56, 46), (72, 58), (89, 73))     # 头 → 胸 → 腹，一条斜线
FLY_FORE = q(FLY_THORAX, (89, 69), (107, 76))  # 短笔：前翅，往前下方伸
FLY_HIND = q(FLY_THORAX, (96, 46), (112, 36))  # 长笔：后翅，往后上方甩
FLY_ANT = [
    q(FLY_HEAD, (40, 36), (28, 22)),            # 触角 1
    q(FLY_HEAD, (46, 30), (40, 12)),            # 触角 2
]

B1 = [FLY_BODY, FLY_FORE, FLY_HIND] + FLY_ANT
B2 = [FLY_BODY, FLY_FORE, FLY_HIND]                     # 不画触角
B3 = [FLY_FORE, FLY_HIND] + FLY_ANT                     # 不画身体
B1R = flip_x(B1)
B2R = flip_x(B2)

# —— C 系列：一长一短两笔（严格照"简单的一长一短线条"来）——
#
# 两笔都从胸部的点出发，短笔当小翅膀、长笔当大翅膀，长短和方向都拉开；
# 可选在胸部点一个小圆当身体。朝右上方飞。

C_THORAX = (52, 68)
C_BASE = [
    q(C_THORAX, (72, 46), (78, 20)),            # 短笔：前翅，偏竖
    q(C_THORAX, (98, 50), (108, 36)),           # 长笔：后翅，长而平
]
C_BASE2 = [
    q(C_THORAX, (70, 44), (74, 16)),            # 短笔：更竖
    q(C_THORAX, (100, 54), (112, 46)),          # 长笔：更平更长
]
# 一长一短，但两笔都往同一侧甩（都朝后上方），像收翅滑翔
C_SWEPT = [
    q(C_THORAX, (84, 40), (96, 18)),            # 短笔
    q(C_THORAX, (102, 46), (116, 30)),          # 长笔
]

# —— G 系列：极简蝴蝶的各种读法（一次铺开对比）——
#
# 前面几轮说明一件事：细线画到"一片闭合的翅 + 一根斜线"就变成叶子/气球/鱼了。
# 所以在 24~32px 还敢认蝴蝶的极简写法，一般得同时给出这几样：
#   翅的**成对感**（能看出两片）、一根**身体**、以及**尾部收窄**。
# 这一批把不同的极简写法并排铺开，直接看哪个在 32px 还站得住。

# G1 侧影 · 大翅 + 小后翅 + 身体 + 触角 + 尾突
G1 = [
    q((60, 58), (34, 34), (26, 56)),        # 大翅上缘
    q((26, 56), (30, 74), (58, 70)),        # 大翅下缘
    q((60, 66), (44, 74), (42, 90)),        # 后翅
    q((42, 90), (52, 100), (62, 88)),       # 后翅下缘
    q((60, 62), (62, 46), (60, 30)),        # 身体（竖直）
    q((60, 30), (52, 22), (46, 14)),        # 触角 1
    q((60, 30), (68, 22), (74, 14)),        # 触角 2
    q((60, 88), (58, 98), (54, 108)),       # 尾突
]

# G2 侧影 · 两片翅都往同一侧张开（都朝上），身体斜着
G2 = [
    q((58, 70), (28, 54), (22, 28)),        # 前翅：往上甩
    q((58, 70), (66, 44), (82, 24)),        # 后翅：往上甩（更长）
    q((58, 70), (50, 86), (40, 102)),       # 身体：往下拖
    q((58, 70), (72, 74), (86, 82)),        # 腹部往后
]

# G3 侧影 · 一长一短两笔 + 圆点（C1 的形），但两笔更贴在一起
G3 = [
    q((46, 76), (58, 48), (62, 22)),
    q((46, 76), (86, 60), (104, 40)),
]
G3_DOTS = [(46, 76, 6.0)]

# G4 侧影 · 大翅一笔勾成，身体一条斜线，尾端收细
G4 = [
    q((56, 74), (30, 40), (52, 24)),        # 翅上缘：从身体绕过翅顶
    q((52, 24), (74, 34), (70, 60)),        # 翅下缘：绕回来
    q((70, 60), (66, 70), (56, 74)),        # 收口
    q((58, 72), (46, 88), (30, 102)),       # 身体/尾：往左下拖长
]

# G5 侧影 · 参考图那味：细长 S 形身体 + 一片圆钝大翅 + 两根细须
G5 = [
    q((64, 30), (44, 24), (30, 40)),        # 翅上缘
    q((30, 40), (22, 54), (36, 64)),        # 翅顶
    q((36, 64), (52, 68), (64, 52)),        # 翅下缘
    q((64, 30), (76, 52), (70, 74)),        # 身体：S 形往下
    q((70, 74), (60, 92), (42, 104)),       # 尾部细线甩出去
    q((68, 40), (92, 46), (106, 56)),       # 细须 1
    q((70, 60), (90, 70), (100, 84)),       # 细须 2
]
G5_DOTS = [(36, 44, 5.0)]

# G6 只两笔（一长一短）+ 两根细须：最克制，也最接近"简单线条"
G6 = [
    q((44, 78), (54, 46), (60, 20)),        # 短笔
    q((44, 78), (92, 62), (110, 44)),       # 长笔
    q((50, 22), (74, 24), (92, 32)),        # 细须 1
    q((50, 30), (68, 38), (82, 50)),        # 细须 2
]

# —— H 系列：实心翅膀的侧影（照参考图的"圆钝大翅"）——
#
# 线描的极简写法一缩到 24px 就散架（闭合轮廓读成叶子/气球/鱼），
# 所以这版把翅膀画成**实心片**：小尺寸下至少还是一坨"蝴蝶翅膀"而不是一根线。
# 多边形必须是简单多边形（不自交），否则 PIL 的填充会互相抵消。

H_BODY = q((52, 26), (40, 66), (30, 104))         # 身体：细长的 S 形往下拖
H_ANT_L = [                                       # 头顶两片小翅（侧视的后翅尖）
    q((52, 30), (34, 26), (28, 12)),
    q((52, 30), (70, 28), (76, 14)),
]
H_ANT = [
    q((52, 26), (62, 14), (74, 8)),               # 触须 1
    q((52, 30), (60, 22), (68, 18)),              # 触须 2
]
# 一片圆钝的大翅（实心），挂在身体右侧
H_WING = [
    (56, 56),
    (72, 34), (96, 26), (102, 36),
    (108, 50), (100, 62), (86, 58),
    (74, 55), (62, 60), (56, 56),
]
# 一片小的后翅（实心），挂在大翅下方
H_WING_LOW = [
    (56, 68),
    (74, 66), (88, 72), (86, 80),
    (84, 88), (70, 86), (58, 78),
    (56, 68),
]

VARIANTS = [
    ("H1 实心大翅+小后翅+身体+触须",
     dict(strokes=[H_BODY] + H_ANT, filled=H_WING + H_WING_LOW,
          dots=[], stroke_w=4.5, scale_w=1.0)),
    ("H2 只有实心大翅+身体+触须",
     dict(strokes=[H_BODY] + H_ANT, filled=H_WING,
          dots=[], stroke_w=4.5, scale_w=1.0)),
    ("H3 大翅更圆更竖",
     dict(strokes=[H_BODY] + H_ANT,
          filled=[(56, 54), (66, 28), (86, 18), (100, 26), (104, 42),
                  (100, 58), (84, 60), (68, 62), (56, 54)] + H_WING_LOW,
          dots=[], stroke_w=4.5, scale_w=1.0)),
    ("H4 实心翅 + 线描身体（无线须）",
     dict(strokes=[H_BODY], filled=H_WING + H_WING_LOW,
          dots=[], stroke_w=4.5, scale_w=1.0)),
    ("H5 两侧各一片翅（朝上张开）",
     dict(strokes=[q((60, 96), (58, 62), (60, 40))] + H_ANT,
          filled=[(58, 50), (44, 20), (18, 12), (10, 26), (16, 44),
                  (34, 48), (50, 52), (58, 50),
                  (62, 50), (76, 20), (102, 12), (110, 26), (104, 44),
                  (86, 48), (70, 52), (62, 50)],
          dots=[], stroke_w=4.5, scale_w=1.0)),
    ("G3 参考：一长一短+圆点", dict(strokes=G3, dots=G3_DOTS,
                                    stroke_w=5.5, scale_w=1.0)),
]

# 蓝色圆点用主题里的 --info（亮色主题 #2563eb）
ACCENT = (0x25, 0x63, 0xEB)


# ---------------------------------------------------------------- 渲染

def variant_bounds(v):
    """该方案所有可见轮廓的包围盒（含线宽、圆点和实心片）。"""
    xs, ys = [], []
    hw = v["stroke_w"] / 2.0
    for ctrl in v["strokes"]:
        pts = R.flatten(ctrl)
        xs += [pts[:, 0].min() - hw, pts[:, 0].max() + hw]
        ys += [pts[:, 1].min() - hw, pts[:, 1].max() + hw]
    for (px, py) in v.get("filled", []):
        xs.append(px)
        ys.append(py)
    for (cx, cy, r) in v.get("dots", []) + v.get("accent_dots", []):
        xs += [cx - r, cx + r]
        ys += [cy - r, cy + r]
    return min(xs), min(ys), max(xs), max(ys)


def variant_mask(size, ss, v):
    """把某个方案渲染成 alpha 蒙版（0..1），复用主脚本的距离场做法。"""
    n = size * ss
    f = n / R.VB

    # 可见轮廓缩放到内缩 MARGIN 的方框里并居中
    x0, y0, x1, y1 = variant_bounds(v)
    vw, vh = x1 - x0, y1 - y0
    inner = R.VB * (1.0 - 2.0 * R.MARGIN)
    scale = min(inner / vw, inner / vh) * v["scale_w"]
    tx = (R.VB - scale * vw) / 2.0 - scale * x0 + R.BIAS_X
    ty = (R.VB - scale * vh) / 2.0 - scale * y0 + R.BIAS_Y

    def T(p):
        return ((tx + scale * p[0]) * f, (ty + scale * p[1]) * f)

    out = np.zeros((n, n), dtype=np.float32)

    # 实心片：直接用多边形填充（小尺寸下实心翅膀比线描轮廓好认得多）
    if v.get("filled"):
        im = Image.new("L", (n, n), 0)
        ImageDraw.Draw(im).polygon([T(p) for p in v["filled"]], fill=255)
        out = np.maximum(out, np.asarray(im, dtype=np.float32) / 255.0)

    segs, spans = [], []
    for ctrl in v["strokes"]:
        pts = np.array([T(p) for p in R.flatten(ctrl)], dtype=np.float32)
        segs.append(pts)
        spans.append((float(pts[:, 1].min()), float(pts[:, 1].max())))

    hw = v["stroke_w"] * scale * f / 2.0
    dots = []
    for (dcx, dcy, dr) in v.get("dots", []):
        px, py = T((dcx, dcy))
        dots.append((px, py, dr * scale * f))
    if not segs and not dots:
        return out

    all_x = [p[:, 0] for p in segs] + [np.array([d[0] - d[2], d[0] + d[2]]) for d in dots]
    all_y = [p[:, 1] for p in segs] + [np.array([d[1] - d[2], d[1] + d[2]]) for d in dots]
    pad = hw + 2.0
    x_lo = int(max(0, np.floor(min(a.min() for a in all_x) - pad)))
    x_hi = int(min(n, np.ceil(max(a.max() for a in all_x) + pad)))
    y_lo = int(max(0, np.floor(min(a.min() for a in all_y) - pad)))
    y_hi = int(min(n, np.ceil(max(a.max() for a in all_y) + pad)))

    out = np.zeros((n, n), dtype=np.float32)
    if x_hi <= x_lo or y_hi <= y_lo:
        return out
    xs = np.arange(x_lo, x_hi, dtype=np.float32) + 0.5

    for cy0 in range(y_lo, y_hi, R.CHUNK):
        cy1 = min(cy0 + R.CHUNK, y_hi)
        PX = xs[None, :]
        PY = (np.arange(cy0, cy1, dtype=np.float32) + 0.5)[:, None]
        py_min, py_max = float(PY.min()) - hw - 4.0, float(PY.max()) + hw + 4.0
        d = np.full((cy1 - cy0, x_hi - x_lo), np.inf, dtype=np.float32)
        for pts, (s_lo, s_hi) in zip(segs, spans):
            if s_lo > py_max or s_hi < py_min:
                continue
            for c0 in range(0, len(pts) - 1, R.SEG_CHUNK):
                sub = pts[c0:c0 + R.SEG_CHUNK + 1]
                ax, ay = sub[:-1, 0][None, None, :], sub[:-1, 1][None, None, :]
                bx, by = sub[1:, 0][None, None, :], sub[1:, 1][None, None, :]
                vx, vy = bx - ax, by - ay
                wx, wy = PX[..., None] - ax, PY[..., None] - ay
                L2 = vx * vx + vy * vy
                L2[L2 == 0] = 1.0
                s = np.clip((wx * vx + wy * vy) / L2, 0.0, 1.0)
                dx = wx - s * vx
                dy = wy - s * vy
                np.minimum(d, np.sqrt(dx * dx + dy * dy).min(axis=2), out=d)
        for (dx0, dy0, dr) in dots:
            np.minimum(d, np.sqrt((PX - dx0) ** 2 + (PY - dy0) ** 2) - dr, out=d)
        d -= hw
        c = np.clip(0.5 - d / R.AA_WIDTH, 0.0, 1.0)
        out[cy0:cy1, x_lo:x_hi] = c * c * (3.0 - 2.0 * c)
    return out


def accent_mask(size, ss, v):
    """品牌点（可选）的 alpha 蒙版，用同一套缩放居中参数。"""
    if not v.get("accent_dots"):
        return None
    n = size * ss
    f = n / R.VB
    x0, y0, x1, y1 = variant_bounds(v)
    vw, vh = x1 - x0, y1 - y0
    inner = R.VB * (1.0 - 2.0 * R.MARGIN)
    scale = min(inner / vw, inner / vh) * v["scale_w"]
    tx = (R.VB - scale * vw) / 2.0 - scale * x0 + R.BIAS_X
    ty = (R.VB - scale * vh) / 2.0 - scale * y0 + R.BIAS_Y

    yy, xx = np.mgrid[0:n, 0:n].astype(np.float32)
    acc = np.zeros((n, n), dtype=np.float32)
    for (dcx, dcy, dr) in v["accent_dots"]:
        px = (tx + scale * dcx) * f
        py = (ty + scale * dcy) * f
        pr = dr * scale * f
        dist = np.sqrt((xx + 0.5 - px) ** 2 + (yy + 0.5 - py) ** 2) - pr
        c = np.clip(0.5 - dist / R.AA_WIDTH, 0.0, 1.0)
        np.maximum(acc, c * c * (3.0 - 2.0 * c), out=acc)
    return acc


def compose(size, ss, v):
    """圆角底座 + 标记，合成一张 RGBA。"""
    ta = R.tile_alpha(size, ss)
    gm = variant_mask(size, ss, v)
    am = accent_mask(size, ss, v)
    bg = np.zeros((size * ss, size * ss, 3), dtype=np.float32)
    bg[:] = np.array(R.BG, dtype=np.float32) / 255.0
    fg = np.zeros_like(bg)
    fg[:] = np.array(R.FG, dtype=np.float32) / 255.0
    stack = gm[..., None] * fg + (1.0 - gm)[..., None] * bg
    if am is not None:
        acc = np.zeros_like(bg)
        acc[:] = np.array(ACCENT, dtype=np.float32) / 255.0
        stack = am[..., None] * acc + (1.0 - am)[..., None] * stack
    arr = np.concatenate([stack, ta[..., None]], axis=2)
    img = Image.fromarray(np.clip(np.rint(arr * 255.0), 0, 255).astype(np.uint8), "RGBA")
    return img.resize((size, size), Image.LANCZOS) if ss > 1 else img


# ---------------------------------------------------------------- 预览图

def sheet(dark: bool):
    S = 192                      # 每个方案的主图尺寸
    col_w = (S, 96, 48, 32, 24, 16)
    gap, pad, label_w = 20, 28, 268
    row_h = S + 26
    W = pad * 2 + label_w + sum(col_w) + gap * (len(col_w) - 1)
    H = pad * 2 + row_h * len(VARIANTS)
    bg = (24, 24, 27) if dark else (246, 246, 247)
    ink = (206, 206, 212) if dark else (48, 48, 54)
    head = (232, 232, 238) if dark else (20, 20, 24)

    canvas = Image.new("RGB", (W, H), bg)
    d = ImageDraw.Draw(canvas)
    d.text((pad, 8), "方案", fill=head)
    for i, (name, v) in enumerate(VARIANTS):
        y = pad + i * row_h
        d.text((pad, y + S // 2 - 8), name, fill=ink)
        base = y + S
        x = pad + label_w
        main = compose(S, 4, v)
        canvas.paste(main, (x, y), main)
        x += S + gap
        for cs in col_w[1:]:
            im = main.resize((cs, cs), Image.LANCZOS)
            canvas.paste(im, (x, base - cs), im)
            x += cs + gap
    out = ICONS / ("butterfly-dark.png" if dark else "butterfly-light.png")
    canvas.save(out)
    return out


def main():
    print(f"渲染 {len(VARIANTS)} 个方案…")
    for dark in (False, True):
        out = sheet(dark)
        print(f"  {out.name}")
    print("每个方案从左到右：192 / 96 / 48 / 32 / 24 / 16 px")


if __name__ == "__main__":
    main()
