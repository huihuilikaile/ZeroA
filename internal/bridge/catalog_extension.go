package bridge

// catalogExtensionSource 是"工具清单"扩展的源码（由程序写入 <数据目录>/extensions/）。
//
// 与 delegate-task.ts 同一套路：地址与令牌从进程环境读，不落盘；
// 真正的读写与校验在 Go 侧（catalog_bridge.go / tool_catalog_write.go）。
// 模型因此可以：盘点环境 → 发现缺工具 → 装好 → 登记进清单，形成自我维护的回路。
const catalogExtensionSource = `/**
 * 工具清单（tool catalog）——让模型自己维护"这台机器上有什么工具"。
 *
 * 由 ZeroA 工作台自动生成，不要手改（改了下一次启动会被覆盖）。
 *
 * 为什么需要：清单最初只有人工维护的一张表，但真实使用里有两件事只有运行期才知道——
 *   1. 工具装在 PATH 之外（工具箱自带目录、便携包、pip 的 Scripts）；
 *   2. 做题过程中会现装新库/新工具，装完不登记，下次启动又变成"本机没找到"。
 * 所以这里给模型四个口子：看清单、跑检测、登记新工具、把实测到的路径写回。
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const url = () => process.env.PI_GUI_DELEGATE_URL ?? "";
const token = () => process.env.PI_GUI_DELEGATE_TOKEN ?? "";

async function call(path: string, body: unknown): Promise<any> {
  const base = url();
  if (!base || !token()) {
    throw new Error("工具清单通道不可用：这个会话不是由 ZeroA 工作台启动的（缺 PI_GUI_DELEGATE_URL/TOKEN）");
  }
  const response = await fetch(base + path, {
    method: "POST",
    headers: { "content-type": "application/json", "x-delegate-token": token() },
    body: JSON.stringify(body ?? {}),
  });
  const data = (await response.json()) as any;
  if (data && data.error) throw new Error(String(data.error));
  return data;
}

function brief(status: any): string {
  const flag = status.available ? "✓" : "✗";
  const where = status.path ? "  " + status.path : "";
  const version = status.version ? "  " + status.version : "";
  const why = status.available ? "" : "  (" + (status.detail ?? "没找到") + ")";
  return flag + " " + status.id + where + version + why;
}

export default function (pi: ExtensionAPI) {
  /**
   * 会话启动时把**真正声明给模型的工具集**写到文件，供工作台读取。
   *
   * 为什么不让工作台自己解析会话系统提示：那段 "Available tools" 只是精选片段
   * （实测连默认启用的 read/edit/write 都不在里面），据此显示"模型可调用的工具"
   * 会又缺又错。真正权威的是 Pi 的 getActiveTools()——只有跑在 Pi 进程里的扩展拿得到。
   */
  pi.on("session_start", async () => {
    try {
      const fs = await import("node:fs/promises");
      const path = await import("node:path");
      const dir = process.env.PI_CODING_AGENT_DIR ?? "";
      if (!dir) return;
      const all = (pi as any).getAllTools?.() ?? [];
      const active = new Set<string>(pi.getActiveTools?.() ?? []);
      const tools = (all as any[]).map((tool: any) => ({
        name: tool.name,
        label: tool.label ?? "",
        description: tool.description ?? "",
        exposure: tool.exposure ?? "direct",
        active: active.has(tool.name),
      }));
      const payload = {
        version: 1,
        capturedAt: new Date().toISOString(),
        pid: process.pid,
        activeCount: tools.filter((tool: any) => tool.active).length,
        tools,
      };
      await fs.writeFile(path.join(dir, "pi-tools.json"), JSON.stringify(payload, null, 2), "utf-8");
    } catch {
      // 上报失败不能影响会话：工作台那边会退回解析会话提示
    }
  });

  pi.registerTool({
    name: "catalog_list",
    label: "Tool catalog: list",
    description:
      "查看当前工具清单（本机环境工具：命令行程序 / Python 包 / 桌面程序），包含 id、名称、方向、能力标签、记录路径与查找线索。" +
      "在准备用某个工具之前，先看这里它是否已被登记，避免重复添加。",
    parameters: Type.Object({}),
    async execute() {
      const data = await call("/catalog/list", {});
      const tools = (data.tools ?? []) as any[];
      const lines = tools.map(
        (tool: any) =>
          tool.id +
          "  [" + (tool.type ?? "?") + "/" + (tool.direction ?? "?") + "]" +
          (tool.capabilities?.length ? "  能力:" + tool.capabilities.join(",") : "") +
          (tool.path ? "  路径:" + tool.path : ""),
      );
      return {
        details: undefined,
        content: [
          {
            type: "text",
            text:
              "清单文件：" + (data.files?.join(" + ") || "(仅内置默认)") +
              "\n搜索目录：" + ((data.searchPaths ?? []).join(" ; ") || "(无)") +
              "\n共 " + tools.length + " 条：\n" + lines.join("\n"),
          },
        ],
      };
    },
  });

  pi.registerTool({
    name: "catalog_check",
    label: "Tool catalog: check",
    description:
      "对清单里每条工具跑一次真实检测（找 PATH、清单记录的路径、额外搜索目录、Python import），" +
      "返回可用/不可用、真实路径与版本；默认会把新发现的路径写回清单（record=false 可只检测不写）。" +
      "在做环境侦察、或不确定某工具到底在不在时用它。",
    parameters: Type.Object({
      record: Type.Optional(Type.Boolean({ description: "是否把找到的路径写回清单，默认 true" })),
    }),
    async execute(_id: string, params: { record?: boolean }) {
      const data = await call("/catalog/check", { record: params.record !== false });
      const statuses = (data.tools ?? []) as any[];
      const missing = statuses.filter((status: any) => !status.available);
      const summary = data.summary ?? {};
      return {
        details: undefined,
        content: [
          {
            type: "text",
            text:
              "检测完成：" + (summary.available ?? 0) + " 可用 / " + (summary.missing ?? 0) + " 不可用（共 " + (summary.total ?? 0) + "）" +
              (data.recorded?.length ? "\n已把新路径写回清单：" + data.recorded.join("、") : "") +
              (data.failures?.length ? "\n写回失败：" + data.failures.join("；") : "") +
              "\n\n不可用：\n" + (missing.length ? missing.map(brief).join("\n") : "（无）"),
          },
        ],
      };
    },
  });

  pi.registerTool({
    name: "catalog_add",
    label: "Tool catalog: add",
    description:
      "把一个工具登记进清单（新装/新找到的都算）。至少给一条查找线索：path（绝对路径，最准）、commands（命令名数组）、" +
      "pythonImport（模块名）、nodeModule（包名）。登记后会自动验一次并告诉你这次是否真的找到了。" +
      "做题时 pip/npm/下载安装了新工具，用完这个再往下做——否则下次启动它又会变成\"本机没找到\"。",
    parameters: Type.Object({
      id: Type.String({ description: "工具 id（小写字母数字与 - _ .），例如 ffuf / volatility3 / my-tool" }),
      name: Type.Optional(Type.String({ description: "显示名，缺省用 id" })),
      path: Type.Optional(Type.String({ description: "可执行文件的绝对路径（最准，PATH 外的工具靠它记住）" })),
      commands: Type.Optional(Type.Array(Type.String(), { description: "命令名，按顺序找，例如 [\"ffuf\",\"ffuf.exe\"]" })),
      pythonImport: Type.Optional(Type.String({ description: "Python 模块名，例如 Crypto" })),
      nodeModule: Type.Optional(Type.String({ description: "Node 包名" })),
      direction: Type.Optional(Type.String({ description: "方向：base/web/pwn/reverse/crypto/forensics/ir/misc" })),
      note: Type.Optional(Type.String({ description: "一句话说明它用来干什么" })),
      capabilities: Type.Optional(Type.Array(Type.String(), { description: "能力标签，例如 [\"web.fuzz\"]；角色声明能力就靠它映射" })),
      versionArgs: Type.Optional(Type.Array(Type.String(), { description: "取版本号的参数，例如 [\"-ver\"]（ExifTool 这类不认 --version）" })),
    }),
    async execute(_id: string, params: any) {
      const data = await call("/catalog/add", { ...params, addedBy: "ai" });
      const status = data.status ?? {};
      return {
        details: undefined,
        content: [
          {
            type: "text",
            text:
              "已登记 " + data.id + " → " + data.path +
              "\n本次验证：" + brief(status) +
              (status.available ? "" : "\n⚠️ 登记成功但这次没找到：检查 path/commands 是否写对，或先把它装好再登记。"),
          },
        ],
      };
    },
  });

  pi.registerTool({
    name: "catalog_record",
    label: "Tool catalog: record path",
    description:
      "把某个工具的真实路径写回清单（相当于 catalog_add 只更新 path）。" +
      "自己用 bash 找到工具位置（where/which/ls）之后，用它把路径记下来，下次就不用再找了。",
    parameters: Type.Object({
      id: Type.String({ description: "清单里的工具 id" }),
      path: Type.String({ description: "实测到的绝对路径" }),
    }),
    async execute(_id: string, params: { id: string; path: string }) {
      const data = await call("/catalog/add", { id: params.id, path: params.path, addedBy: "ai" });
      const status = data.status ?? {};
      return {
        details: undefined,
        content: [
          {
            type: "text",
            text: "已记录 " + data.id + " 的路径：" + params.path + "\n本次验证：" + brief(status),
          },
        ],
      };
    },
  });
}
`
