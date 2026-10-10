#!/usr/bin/env python3
"""生成「水母 π」图标 —— 一个整体右倾、两条腿像水母触手一样向外弯的 π。

需求
----
π 主体、整体向右倾斜、下面两条腿在底部弯曲像水母、图标带圆角、背景透明。

设计
----
* **π** = 一根横杠 + 两条竖腿（几何画出来，不是字体字形，所以任何机器上长得都一样）。
* **两条腿弯曲像水母** = 竖腿下到 2/3 处在底部向外弯出去、笔尖微微上翘，
  像水母拖在身后的触手；左右镜像，保持图标对称。
* **整体向右倾斜** = 对图形做斜切（shear，Slant 度）。斜切不改变水平线，
  所以横杠保持水平、两条腿与中轴一起右倾——和真正的意大利体斜体同理。
* **圆角** = squircle 底座（超椭圆 n=5），跟 build/icons/render_icons.py 的
  蝴蝶图标同一套底座；方块之外 alpha=0，是真透明，不是白底。
* **圆头笔画** = 到折线的精确距离场（不是 PIL 折线扫描），
  圆端帽 / 圆拐角在数学上精确，4x 超采样后再降采样。

产物（全部落在 build/icons/jellyfish/）
--------------------------------------
  pi-jellyfish.svg          母版（squircle 底座 + 水母 π），透明背景
  pi-jellyfish-mark.svg     只要图形、不要底座（纯透明背景）
  pi-jellyfish-<size>.png   图标阶梯 1024/512/256/128/64/48/32/24/16
  pi-jellyfish-mark-*.png   图形阶梯（无底座）
  pi-jellyfish.ico          Windows 多尺寸（16/24/32/48/64/128/256）
  preview-light/dark.png    预览版式（自检用，证明圆角外是真透明）

本脚本只用 numpy + Pillow，纯本地计算，不联网、不起子进程。
"""

from __future__ import annotations

import math
from collections import namedtuple
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

# ---------------------------------------------------------------- 基本参数

HERE = Path(__file__).resolve().parent

VB = 120.0              # 母版画布边长（跟 render_icons.py 一致）
ART_CX = 50.0           # 图形坐标系中心（图形定义在 0..100 里）
ART_CY = 50.0
SS = 4                  # 超采样倍数
SQUIRCLE_N = 5.0        # 超椭圆指数（跟蝴蝶图标同一套）
PAD = 0.045             # 圆角方块四周留的透明边
FLATTEN_STEPS = 160     # 每段三次贝塞尔采样成多少段折线
AA_WIDTH = 1.0          # 距离场 → 覆盖率的过渡带宽度（渲染像素）
CHUNK = 512             # 距离场分带高度
SEG_CHUNK = 64          # 一次算多少段折线

# 里外配色都取 frontend/src/styles/tokens.css
BG_DARK = (0x15, 0x15, 0x17)      # --bg（暗色主题）
FG_DARK = (0xF9, 0xFA, 0xFB)      # --text（暗色主题）
BG_LIGHT = (0xFF, 0xFF, 0xFF)     # --bg（亮色主题）
FG_LIGHT = (0x0D, 0x0D, 0x0D)     # --text（亮色主题）

# 放置：图形可见轮廓到画布边缘的最小留白
TILE_SAFETY = 0.93      # 图形外接框必须落在 squircle 内的安全系数
MARGIN_TILE = 0.055     # 图标版：图形外接框到画布边缘的留白
MARGIN_MARK = 0.070     # 无底座版：图形自己撑满画布，留白给大一点

SIZES = ((1024, 4), (512, 4), (256, 4), (128, 4), (64, 4),
         (48, 4), (32, 8), (24, 8), (16, 16))
MARK_SIZES = ((1024, 4), (512, 4), (256, 4), (128, 4), (64, 4), (32, 8))
ICO_SIZES = (16, 24, 32, 48, 64, 128, 256)

# ---------------------------------------------------------------- 图形定义
#
# 坐标都在 0..100 的图形坐标系里，y 轴向下。
#
#   * 横杠      y=24，x 从 16 到 84（比两条腿各外伸 18）
#   * 竖腿      从横杠下方 x=34 / x=66 出发（只在左腿上定义，右腿镜像）
#   * 触手弯    竖腿先几乎竖直下到 y≈71，再向外弯出去：
#               最低点 y≈86 落在 x≈18，笔尖收到 (10, 85) 附近、
#               方向朝外略向上——水母触手那一下"甩"。
#
# Design 字段：
#   name       变体名（预览表里显示）
#   slant_deg  斜切角度（意大利体那种"竖画歪、横画平"的倾斜）
#   rotate_deg 整体旋转角度（顺时针为正）——草图要的是这一种：
#              横杠自己也斜下来，整个 π 像在往右倾倒 / 往前游
#   stroke_w   腿的笔画粗细（图形坐标）
#   bar_w      横杠的笔画粗细；None = 跟腿一样粗。
#              横杠比腿粗一点会读成"伞盖 + 触手"，是水母味的关键一招。
#   bar        横杠两点 ((x0,y),(x1,y))
#   leg        左腿的三次贝塞尔链：起点 + 每段 3 个控制点
#   leg_style  "mirror" = 两条腿左右对称地向外撇；
#              "trail"  = 两条腿平行、一起往左甩（水母游动时拖在身后的触手）
#   note       备注

Design = namedtuple("Design", "name slant_deg rotate_deg stroke_w bar_w bar leg leg_style note")

LEG_TAIL = ((34.0, 24.0),
            (34.0, 46.0), (34.0, 60.0), (30.0, 71.0),
            (26.0, 82.0), (19.0, 89.0), (10.0, 85.0))

# 腿更长、弯得更狠：草图上两条触手都比横杠长（117/127 vs 73），
# 而且从近乎竖直一路弯到接近水平。
LEG_LONG = ((34.0, 24.0),
            (34.0, 50.0), (35.0, 66.0), (31.0, 78.0),
            (27.0, 90.0), (20.0, 98.0), (10.0, 94.0))

BASE = Design(
    name="A 基准",
    slant_deg=15.0,
    rotate_deg=0.0,
    stroke_w=9.0,
    bar_w=None,
    bar=((16.0, 24.0), (84.0, 24.0)),
    leg=LEG_TAIL,
    leg_style="mirror",
    note="15° 斜切 / 笔画 9 / 触手外弯微翘",
)

VARIANTS = (
    BASE,
    BASE._replace(name="B 细笔画", stroke_w=7.5, note="笔画 7.5，更秀气"),
    BASE._replace(name="C 粗笔画", stroke_w=10.5, note="笔画 10.5，更敦实"),
    BASE._replace(name="D 大倾斜", slant_deg=20.0, note="20° 斜切，动感更强"),
    BASE._replace(name="E 小倾斜", slant_deg=11.0, note="11° 斜切，更稳"),
    BASE._replace(
        name="F 上翘笔尖", stroke_w=9.0,
        leg=((34.0, 24.0),
             (34.0, 46.0), (34.0, 60.0), (30.0, 71.0),
             (26.0, 82.0), (17.0, 90.0), (9.0, 80.0)),
        note="触手笔尖明显上翘（水母感更强）",
    ),
    BASE._replace(
        name="G 平出笔尖", stroke_w=9.0,
        leg=((34.0, 24.0),
             (34.0, 46.0), (34.0, 60.0), (30.0, 71.0),
             (25.0, 83.0), (17.0, 89.0), (7.0, 86.0)),
        note="触手几乎水平甩出",
    ),
    BASE._replace(
        name="H 窄横杠", stroke_w=9.0,
        bar=((20.0, 24.0), (80.0, 24.0)),
        leg=((35.0, 24.0),
             (35.0, 46.0), (35.0, 60.0), (31.0, 71.0),
             (27.0, 82.0), (20.0, 89.0), (13.0, 85.0)),
        note="横杠收窄到 60，整体更方",
    ),
)

# ---------------------------------------------------------------- 第二轮：触手形态
#
# 第一轮（A..H）证明"横杠 + 外弯腿"能读成 π，但触手味还不够。
# 这一轮专攻腿的形状：内卷笔尖、S 形波、缓起弧、"伞盖比触手粗"。

VARIANTS_2 = (
    BASE._replace(
        name="I 内卷笔尖",
        leg=((34.0, 24.0),
             (34.0, 46.0), (35.0, 60.0), (32.0, 71.0),
             (29.0, 82.0), (24.0, 90.0), (15.0, 88.0),
             (9.0, 86.0), (6.0, 81.0), (9.0, 75.0)),
        note="笔尖像蕨叶一样往回卷",
    ),
    BASE._replace(
        name="J 伞盖加粗", stroke_w=8.0, bar_w=11.0,
        note="横杠 11 / 腿 8：伞盖 + 触手",
    ),
    BASE._replace(
        name="K S 形触手",
        leg=((34.0, 24.0),
             (34.0, 44.0), (36.0, 56.0), (33.0, 66.0),
             (30.0, 76.0), (25.0, 86.0), (16.0, 88.0),
             (9.0, 89.0), (5.0, 83.0), (8.0, 76.0)),
        note="先内收再外摆，像飘动的触手",
    ),
    BASE._replace(
        name="L 缓起弧",
        leg=((34.0, 24.0),
             (35.0, 42.0), (35.0, 58.0), (32.0, 70.0),
             (28.0, 82.0), (20.0, 90.0), (11.0, 86.0)),
        note="更早开始起弧，整条腿都是曲线",
    ),
    BASE._replace(
        name="M 宽摆触手", stroke_w=9.5,
        leg=((34.0, 24.0),
             (34.0, 48.0), (33.0, 62.0), (28.0, 73.0),
             (23.0, 84.0), (15.0, 91.0), (6.0, 86.0)),
        note="触手往外甩得更开",
    ),
    BASE._replace(
        name="N 细长触手", stroke_w=7.0, bar_w=10.0,
        leg=((33.0, 24.0),
             (33.0, 48.0), (34.0, 62.0), (31.0, 73.0),
             (28.0, 84.0), (22.0, 91.0), (13.0, 87.0)),
        note="伞盖 10 / 触手 7：对比最强",
    ),
)

# ---------------------------------------------------------------- 第四轮：照草图量出来的比例
#
# 把草图横杠摆正到图形坐标系之后（见 _sketch_frame.py 的输出）：
#   * 横杠 64 长；两条腿弧长 107 / 125 —— 腿是横杠的 1.7~1.9 倍（我原来只有 1.24 倍）；
#   * 腿的起笔方向 56°~63°（不是垂直于横杠的 90°）→ 说明还带一点斜切；
#   * 中段甩平到 25°~30°，收笔又往下钩（45°~83°）。
# 所以这一轮：腿加长到 ~103，腿距收到 30，斜切加大到 25° 左右。

LEG4 = ((35.0, 24.0),
        (35.0, 48.0), (36.0, 64.0), (32.0, 78.0),
        (27.0, 96.0), (-1.0, 96.0), (-13.0, 116.0))

BAR4 = ((16.0, 24.0), (84.0, 24.0))

VARIANTS_4 = (
    BASE._replace(name="W 只转 28°", slant_deg=0.0, rotate_deg=28.0,
                  leg_style="trail", bar=BAR4, leg=LEG4, stroke_w=9.0,
                  note="不加斜切：腿相对横杠是垂直的"),
    BASE._replace(name="X 切 15° 转 28°", slant_deg=15.0, rotate_deg=28.0,
                  leg_style="trail", bar=BAR4, leg=LEG4, stroke_w=9.0,
                  note="中等斜切"),
    BASE._replace(name="Y 切 25° 转 28°", slant_deg=25.0, rotate_deg=28.0,
                  leg_style="trail", bar=BAR4, leg=LEG4, stroke_w=9.0,
                  note="斜切 25°：腿起笔 ~65°，接近草图"),
    BASE._replace(name="Z 切 25° 转 24°", slant_deg=25.0, rotate_deg=24.0,
                  leg_style="trail", bar=BAR4, leg=LEG4, stroke_w=9.0,
                  note="旋转略小，整体稍稳"),
    BASE._replace(name="AA 切 30° 转 28°", slant_deg=30.0, rotate_deg=28.0,
                  leg_style="trail", bar=BAR4, leg=LEG4, stroke_w=9.0,
                  note="斜切更大：腿更斜、更甩"),
    BASE._replace(name="AB 切 25° 转 32°", slant_deg=25.0, rotate_deg=32.0,
                  leg_style="trail", bar=BAR4, leg=LEG4, stroke_w=9.0,
                  note="旋转更大，倾倒感更强"),
    BASE._replace(name="AC 切 25° 对称腿", slant_deg=25.0, rotate_deg=28.0,
                  leg_style="mirror", bar=BAR4, leg=LEG4, stroke_w=9.0,
                  note="对照组：两条腿左右分开"),
    BASE._replace(name="AD 切 25° 粗 10", slant_deg=25.0, rotate_deg=28.0,
                  leg_style="trail", bar=BAR4, leg=LEG4, stroke_w=10.0,
                  note="线更粗，小尺寸更清楚"),
)
# ---------------------------------------------------------------- 第三轮：整体旋转
#
# 用户草图（273×246）量出来的事实：
#   * 横杠本身是斜的：28°，朝右下（不是"横杠水平、只有竖画歪"的斜体）；
#   * 两条腿的笔画都往左下甩（117 / 127 长 vs 横杠 73），即触手拖在身后；
#   * 腿从近乎竖直（82°）一路弯到接近水平（12°）才收笔。
# 所以这一轮改成「整体顺时针旋转」+「两条腿同向拖尾」，并按草图的粗细/长短配比。

VARIANTS_3 = (
    BASE._replace(name="O 不转", rotate_deg=0.0, leg_style="trail",
                  bar=((16.0, 24.0), (84.0, 24.0)), leg=LEG_LONG,
                  note="0°：只拖尾不旋转，做对照"),
    BASE._replace(name="P 转 18°", rotate_deg=18.0, leg_style="trail",
                  bar=((16.0, 24.0), (84.0, 24.0)), leg=LEG_LONG,
                  note="轻微右倾"),
    BASE._replace(name="Q 转 24°", rotate_deg=24.0, leg_style="trail",
                  bar=((16.0, 24.0), (84.0, 24.0)), leg=LEG_LONG,
                  note="接近草图的右倾"),
    BASE._replace(name="R 转 28°", rotate_deg=28.0, leg_style="trail",
                  bar=((16.0, 24.0), (84.0, 24.0)), leg=LEG_LONG,
                  note="草图横杠的角度"),
    BASE._replace(name="S 转 34°", rotate_deg=34.0, leg_style="trail",
                  bar=((16.0, 24.0), (84.0, 24.0)), leg=LEG_LONG,
                  note="更强的倾倒感"),
    BASE._replace(name="T 转 28° 对称", rotate_deg=28.0, leg_style="mirror",
                  bar=((16.0, 24.0), (84.0, 24.0)), leg=LEG_LONG,
                  note="同样 28°，但两条腿左右对称外撇"),
    BASE._replace(name="U 转 28° 斜切 12", rotate_deg=28.0, slant_deg=12.0,
                  leg_style="trail",
                  bar=((16.0, 24.0), (84.0, 24.0)), leg=LEG_LONG,
                  note="旋转 + 一点斜切，腿更竖"),
    BASE._replace(name="V 转 28° 近方", rotate_deg=28.0, leg_style="trail",
                  bar=((20.0, 24.0), (80.0, 24.0)), leg=LEG_LONG,
                  note="横杠收窄，整体更接近方形"),
)


# ---------------------------------------------------------------- 曲线工具

def cubic_poly(p0, c1, c2, p3, steps=FLATTEN_STEPS):
    """一段三次贝塞尔 → 折线。"""
    t = (np.arange(steps + 1, dtype=np.float64) / steps)[:, None]
    u = 1.0 - t
    p0, c1, c2, p3 = (np.asarray(p, dtype=np.float64) for p in (p0, c1, c2, p3))
    return (u ** 3) * p0 + 3.0 * (u ** 2) * t * c1 + 3.0 * u * (t ** 2) * c2 + (t ** 3) * p3


def chain_poly(chain, steps=FLATTEN_STEPS):
    """起点 + N 段三次贝塞尔的控制点链 → 折线（各段首尾相接，去掉重复点）。"""
    pts = [np.asarray(chain[0], dtype=np.float64)[None, :]]
    rest = chain[1:]
    assert len(rest) % 3 == 0 and rest, "贝塞尔链必须是 起点 + 3n 个控制点"
    for i in range(0, len(rest), 3):
        seg = cubic_poly(chain[0] if i == 0 else rest[i - 1], *rest[i:i + 3], steps=steps)
        if i:
            seg = seg[1:]
        pts.append(seg)
    return np.concatenate(pts, axis=0)


def mirror_x(pts, axis=ART_CX):
    """关于竖直线 axis 镜像。"""
    out = pts.copy()
    out[:, 0] = 2.0 * axis - pts[:, 0]
    return out


def _mat_mul(m2, m1):
    """2x3 仿射矩阵相乘：m2 ∘ m1（先 m1 后 m2）。"""
    a2, b2, c2, d2, e2, f2 = m2
    a1, b1, c1, d1, e1, f1 = m1
    return (a2 * a1 + c2 * b1, b2 * a1 + d2 * b1,
            a2 * c1 + c2 * d1, b2 * c1 + d2 * d1,
            a2 * e1 + c2 * f1 + e2, b2 * e1 + d2 * f1 + f2)


def _mat_shear(deg, cx=ART_CX, cy=ART_CY):
    """斜切（意大利体）：x' = x - (y-cy)·tan(deg)，水平线保持不变。"""
    k = math.tan(math.radians(deg))
    return (1.0, 0.0, -k, 1.0, k * cy, 0.0)


def _mat_rotate(deg, cx=ART_CX, cy=ART_CY):
    """整体旋转：deg 为正 = 屏幕上顺时针（y 轴向下时就是"向右倾倒"）。"""
    t = math.radians(deg)
    co, si = math.cos(t), math.sin(t)
    return (co, si, -si, co,
            cx - cx * co + cy * si,
            cy - cx * si - cy * co)


def transform_matrix(d: Design):
    """Design → 2x3 仿射矩阵 (a,b,c,d,e,f)。

    先斜切再整体旋转：斜切负责"竖画歪、横画平"的斜体感，
    旋转负责"连横杠一起斜下来"的整体右倾（草图要的是后者）。
    """
    m = _mat_shear(d.slant_deg)
    if d.rotate_deg:
        m = _mat_mul(_mat_rotate(d.rotate_deg), m)
    return m


def apply_matrix(pts, m):
    a, b, c, dd, e, f = m
    out = np.empty_like(pts)
    out[:, 0] = a * pts[:, 0] + c * pts[:, 1] + e
    out[:, 1] = b * pts[:, 0] + dd * pts[:, 1] + f
    return out


def design_paths(d: Design):
    """Design → [(折线, 线宽)]，折线已经把斜切 + 整体旋转都做完了。

    leg_style="mirror"：右腿 = 左腿镜像（左右对称外撇）
    leg_style="trail" ：右腿 = 左腿平移一个腿距（两条腿平行，一起往左甩）
    """
    m = transform_matrix(d)
    leg_l = chain_poly(d.leg)
    if d.leg_style == "mirror":
        leg_r = mirror_x(leg_l)
    else:
        # 平移一个腿距：两条腿形状完全一样，只是错开（平行拖尾）
        leg_r = leg_l + np.array([2.0 * (ART_CX - float(d.leg[0][0])), 0.0])

    bar = np.asarray(d.bar, dtype=np.float64)
    bar_w = d.stroke_w if d.bar_w is None else d.bar_w
    return [(apply_matrix(bar, m), bar_w),
            (apply_matrix(leg_l, m), d.stroke_w),
            (apply_matrix(leg_r, m), d.stroke_w)]


# ---------------------------------------------------------------- 放置

def visible_bounds(parts):
    """所有可见轮廓的边界（含线宽与圆端帽）。"""
    xs, ys = [], []
    for p, w in parts:
        hw = w / 2.0
        xs += [p[:, 0].min() - hw, p[:, 0].max() + hw]
        ys += [p[:, 1].min() - hw, p[:, 1].max() + hw]
    return min(xs), min(ys), max(xs), max(ys)


def _solve(d, mode):
    """求 (scale, tx, ty)：图形等比缩放后落在画布中间。

    mode="tile"  图形外接框的四角必须留在 squircle 内（否则圆角会把笔画切掉）；
                 闭式解：外接框半宽/半高 (sw,sh) 满足 (sw/A)^n + (sh/A)^n = 1，
                 于是 s = 2A / (vw^n + vh^n)^(1/n)。
    mode="mark"  没有底座，直接按画布留白放。
    """
    parts = design_paths(d)
    x0, y0, x1, y1 = visible_bounds(parts)
    vw, vh = x1 - x0, y1 - y0

    if mode == "tile":
        a = VB / 2.0 * (1.0 - PAD) * TILE_SAFETY
        n = SQUIRCLE_N
        s = 2.0 * a / (vw ** n + vh ** n) ** (1.0 / n)
        s = min(s, VB * (1.0 - 2.0 * MARGIN_TILE) / max(vw, vh))
    else:
        s = VB * (1.0 - 2.0 * MARGIN_MARK) / max(vw, vh)

    tx = (VB - s * vw) / 2.0 - s * x0
    ty = (VB - s * vh) / 2.0 - s * y0
    return round(s, 6), round(tx, 6), round(ty, 6)


def placement(d, mode="tile"):
    return _solve(d, mode)


# ---------------------------------------------------------------- 栅格化

def squircle_poly(a, samples=4096):
    """超椭圆参数曲线（缩放系数把曲线拉回 (a,0)/(0,a)）。"""
    k = 1.0 / (math.cos(math.pi / SQUIRCLE_N) ** (2.0 / SQUIRCLE_N))
    t = 2.0 * math.pi * np.arange(samples) / samples
    c, s = np.cos(t), np.sin(t)
    x = VB / 2.0 + k * a * np.copysign(np.abs(c) ** (2.0 / SQUIRCLE_N), c)
    y = VB / 2.0 + k * a * np.copysign(np.abs(s) ** (2.0 / SQUIRCLE_N), s)
    return np.stack([x, y], axis=1)


def tile_alpha(size, ss):
    """在 size*ss 分辨率上画抗锯齿的圆角方块 alpha（0..1）。"""
    n = size * ss
    pts = squircle_poly(VB / 2.0 * (1.0 - PAD))
    img = Image.new("L", (n, n), 0)
    ImageDraw.Draw(img).polygon([(x * n / VB, y * n / VB) for x, y in pts], fill=255)
    return np.asarray(img, dtype=np.float32) / 255.0


def glyph_mask(size, ss, parts, scale, tx, ty):
    """在 size*ss 分辨率上画抗锯齿的图形 alpha（0..1）。

    走"到折线的精确距离场"：圆端帽 / 圆拐角数学上精确，
    放大后也不会像 PIL 折线扫描那样在笔画里留下梳齿状条纹。
    不同线宽各算一张 alpha 再取 max（并集）。
    """
    n = size * ss
    f = n / VB

    groups: dict[float, list[np.ndarray]] = {}
    for p, w in parts:
        px = np.array([((tx + scale * x) * f, (ty + scale * y) * f) for x, y in p],
                      dtype=np.float32)
        groups.setdefault(float(w), []).append(px)

    out = np.zeros((n, n), dtype=np.float32)
    for w, segs in groups.items():
        hw = w * scale * f / 2.0
        lo = np.min(np.stack([s.min(axis=0) for s in segs]), axis=0)
        hi = np.max(np.stack([s.max(axis=0) for s in segs]), axis=0)

        pad = hw + 2.0
        x_lo = int(max(0, math.floor(lo[0] - pad)))
        x_hi = int(min(n, math.ceil(hi[0] + pad)))
        y_lo = int(max(0, math.floor(lo[1] - pad)))
        y_hi = int(min(n, math.ceil(hi[1] + pad)))
        if x_hi <= x_lo or y_hi <= y_lo:
            continue

        xs = np.arange(x_lo, x_hi, dtype=np.float32) + 0.5
        spans = [(float(p[:, 1].min()), float(p[:, 1].max())) for p in segs]

        for y0 in range(y_lo, y_hi, CHUNK):
            y1 = min(y0 + CHUNK, y_hi)
            PX = xs[None, :]
            PY = (np.arange(y0, y1, dtype=np.float32) + 0.5)[:, None]
            py_min, py_max = float(PY.min()) - hw - 4.0, float(PY.max()) + hw + 4.0
            d = np.full((y1 - y0, x_hi - x_lo), np.inf, dtype=np.float32)
            for pts, (seg_lo, seg_hi) in zip(segs, spans):
                if seg_lo > py_max or seg_hi < py_min:
                    continue
                for c0 in range(0, len(pts) - 1, SEG_CHUNK):
                    sub = pts[c0:c0 + SEG_CHUNK + 1]
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
            d -= hw                              # 有符号距离：到笔画边缘
            c = np.clip(0.5 - d / AA_WIDTH, 0.0, 1.0)
            cov = c * c * (3.0 - 2.0 * c)
            out[y0:y1, x_lo:x_hi] = np.maximum(out[y0:y1, x_lo:x_hi], cov)
    return out


def compose(size, ss, d, mode="tile", dark=True):
    """渲染一张 size×size 的 RGBA。mode="tile" 带圆角底座，mode="mark" 纯透明底。"""
    scale, tx, ty = placement(d, mode)
    parts = design_paths(d)
    gm = glyph_mask(size, ss, parts, scale, tx, ty)

    bg = np.array(BG_DARK if dark else BG_LIGHT, dtype=np.float32) / 255.0
    fg = np.array(FG_DARK if dark else FG_LIGHT, dtype=np.float32) / 255.0

    if mode == "tile":
        ta = tile_alpha(size, ss)
        top = gm[..., None] * fg + (1.0 - gm)[..., None] * bg
    else:
        # 无底座：整个画布除了笔画本身都是透明的。
        # alpha 直接用笔画的覆盖率，RGB 取前景色（这样抗锯齿边缘不会发灰）。
        ta = gm
        top = np.broadcast_to(fg, gm.shape + (3,)).copy()

    out = np.concatenate([top, ta[..., None]], axis=2)
    img = Image.fromarray(np.clip(np.rint(out * 255.0), 0, 255).astype(np.uint8), "RGBA")
    if ss > 1:
        img = img.resize((size, size), Image.LANCZOS)
    return img


def render(size, extra_ss, d=BASE, mode="tile", dark=True):
    """渲染（内存吃紧时自动降超采样倍数，保证小尺寸也干净）。"""
    for ss in (extra_ss, 4, 2, 1):
        if ss < 1:
            continue
        try:
            return compose(size, ss, d, mode=mode, dark=dark)
        except MemoryError:
            continue
    raise RuntimeError(f"{size}px 渲染失败：内存不足")


# ---------------------------------------------------------------- SVG

def _num(v):
    return f"{v:g}"


def _svg_path(chain):
    """贝塞尔链 → SVG path 的 d（M / L / C ...），坐标是图形坐标系原样。

    斜切与旋转交给 <g> 上的 matrix(...)，SVG 里就是标准的仿射变换。
    """
    pts = [(float(p[0]), float(p[1])) for p in chain]
    d = f"M{_num(pts[0][0])} {_num(pts[0][1])}"
    rest = pts[1:]
    if len(rest) == 1:                      # 横杠：直线段
        return d + f" L{_num(rest[0][0])} {_num(rest[0][1])}"
    for i in range(0, len(rest), 3):
        d += "".join(f" C{_num(x)} {_num(y)}" for x, y in rest[i:i + 3])
    return d


def write_svg(d: Design = BASE):
    """写两个 SVG：带底座母版 + 无底座图形版。几何用 path，斜切/旋转用 matrix。"""
    scale_t, tx_t, ty_t = placement(d, "tile")
    scale_m, tx_m, ty_m = placement(d, "mark")
    mat = " ".join(_num(v) for v in transform_matrix(d))
    bar_w = d.stroke_w if d.bar_w is None else d.bar_w
    if d.leg_style == "mirror":
        mirror_leg = tuple((2.0 * ART_CX - float(p[0]), float(p[1])) for p in d.leg)
    else:
        dx = 2.0 * (ART_CX - float(d.leg[0][0]))
        mirror_leg = tuple((float(p[0]) + dx, float(p[1])) for p in d.leg)

    def mark_group(scale, tx, ty):
        items = ((_svg_path((d.bar[0], d.bar[1])), bar_w),
                 (_svg_path(d.leg), d.stroke_w),
                 (_svg_path(mirror_leg), d.stroke_w))
        paths = "\n".join(f'      <path stroke-width="{_num(w)}" d="{p}" />' for p, w in items)
        return (f'    <g transform="translate({_num(tx)}, {_num(ty)}) scale({_num(scale)})'
                f' matrix({mat})"\n'
                f'       fill="none" stroke="#{FG_DARK[0]:02X}{FG_DARK[1]:02X}{FG_DARK[2]:02X}"\n'
                f'       stroke-linecap="round" stroke-linejoin="round">\n'
                f'{paths}\n'
                f'    </g>')

    poly = " ".join(f"{_num(x)},{_num(y)}" for x, y in squircle_poly(VB / 2.0 * (1.0 - PAD), 720))
    bg_hex = f"#{BG_DARK[0]:02X}{BG_DARK[1]:02X}{BG_DARK[2]:02X}"
    fg_hex = f"#{FG_DARK[0]:02X}{FG_DARK[1]:02X}{FG_DARK[2]:02X}"

    head = f"""<!--
  Pi Agent Workspace「水母 π」图标（母版 · viewBox {VB:g}×{VB:g}）
  =====================================================
  π：横杠 y={_num(d.bar[0][1])}、x {_num(d.bar[0][0])}→{_num(d.bar[1][0])}（线宽 {_num(bar_w)}）；
     两条腿从 x={_num(d.leg[0][0])} 出发、间隔 {_num(2 * (ART_CX - d.leg[0][0]))}，线宽 {_num(d.stroke_w)}。
  水母：腿从近乎竖直一路弯到接近水平，像水母拖在身后的触手
        （leg_style={d.leg_style}：{"两条腿平行同向拖尾" if d.leg_style == "trail" else "两条腿左右对称外撇"}）。
  右倾：斜切 {_num(d.slant_deg)}°（意大利体）＋ 整体顺时针旋转 {_num(d.rotate_deg)}°，
        由 <g> 上的 matrix({mat}) 表达；顺时针旋转会让横杠自己也斜下来。
  圆角：squircle（超椭圆 n={SQUIRCLE_N:g}），方块之外 alpha=0，真透明。
  颜色：tokens.css 暗色主题 --bg {bg_hex} / --text {fg_hex}。
  这个文件是「源」，PNG/ICO 都由它派生：python build/icons/jellyfish/render_jellyfish.py
-->"""
    title = "Pi Agent Workspace · jellyfish π"

    master = f"""<?xml version="1.0" encoding="UTF-8"?>
{head}
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {VB:g} {VB:g}" width="{VB:g}" height="{VB:g}">
  <title>{title}</title>

  <!-- 圆角底座：squircle，外面保持透明 -->
  <path fill="{bg_hex}" d="M{poly} Z" />

  <!-- 水母 π：横杠 + 两条弯腿，圆头笔画 -->
{mark_group(scale_t, tx_t, ty_t)}
</svg>
"""
    mark = f"""<?xml version="1.0" encoding="UTF-8"?>
{head}
  说明：这一份**没有底座**，整个画布透明，只有 π 本身（适合放在自己的按钮/标题栏里）。
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {VB:g} {VB:g}" width="{VB:g}" height="{VB:g}">
  <title>{title}（透明背景）</title>

{mark_group(scale_m, tx_m, ty_m)}
</svg>
"""
    p1 = HERE / "pi-jellyfish.svg"
    p2 = HERE / "pi-jellyfish-mark.svg"
    p1.write_text(master, encoding="utf-8")
    p2.write_text(mark, encoding="utf-8")
    return p1, p2


# ---------------------------------------------------------------- 自检 / 输出

def report(images, label):
    print(f"── {label} ──")
    print("尺寸      可见范围(不含透明)    不透明占比   角/心 alpha")
    for size in sorted(images, reverse=True):
        a = np.asarray(images[size])[..., 3]
        ys, xs = np.nonzero(a > 8)
        if len(xs) == 0:
            print(f"{size:>5}      <全透明!>")
            continue
        bbox = (int(xs.min()), int(ys.min()), int(xs.max()), int(ys.max()))
        frac = float((a > 8).mean())
        print(f"{size:>5}      {str(bbox):<22} {frac:6.1%}      "
              f"{int(a[0, 0])}/{int(a[size // 2, size // 2])}")


def main():
    d = BASE
    bar_w = d.stroke_w if d.bar_w is None else d.bar_w
    print(f"设计         : {d.name} — {d.note}")
    print(f"右倾         : 斜切 {_num(d.slant_deg)}° + 整体旋转 {_num(d.rotate_deg)}°"
          f"（矩阵 {' '.join(_num(v) for v in transform_matrix(d))}）")
    print(f"笔画         : 横杠 {_num(bar_w)} · 腿 {_num(d.stroke_w)}（图形坐标 0..100）；"
          f"腿型 {d.leg_style}")

    for mode in ("tile", "mark"):
        s, tx, ty = placement(d, mode)
        x0, y0, x1, y1 = visible_bounds(design_paths(d))
        print(f"放置[{mode:<4}]  : scale={s:<8} → 落在 x {tx + s * x0:6.2f}..{tx + s * x1:6.2f}"
              f"  y {ty + s * y0:6.2f}..{ty + s * y1:6.2f}（画布 {VB:g}）")

    svg_master, svg_mark = write_svg(d)

    images = {}
    for size, extra in SIZES:
        img = render(size, extra, d, "tile")
        images[size] = img
        img.save(HERE / f"pi-jellyfish-{size}.png", optimize=True)

    marks = {}
    for size, extra in MARK_SIZES:
        img = render(size, extra, d, "mark")
        marks[size] = img
        img.save(HERE / f"pi-jellyfish-mark-{size}.png", optimize=True)

    # Windows 多尺寸 ICO：帧全部来自本脚本重新栅格化的 PNG，不是缩放母版
    ico_path = HERE / "pi-jellyfish.ico"
    frames = [images[s].copy() for s in ICO_SIZES if s in images]
    frames.sort(key=lambda im: im.size[0], reverse=True)
    frames[0].save(ico_path, format="ICO",
                   sizes=[(im.size[0], im.size[1]) for im in frames],
                   append_images=frames[1:])

    print("\n生成：")
    for p in (svg_master, svg_mark, ico_path):
        print(f"  {p.name}  ({p.stat().st_size:,} B)")
    for size, _ in SIZES:
        p = HERE / f"pi-jellyfish-{size}.png"
        print(f"  {p.name}  ({p.stat().st_size:,} B)")
    for size, _ in MARK_SIZES:
        print(f"  pi-jellyfish-mark-{size}.png")

    print()
    report(images, "图标（squircle 底座 + 水母 π）")
    print()
    report(marks, "图形（无底座 · 全透明背景）")
    return images, marks


if __name__ == "__main__":
    main()
