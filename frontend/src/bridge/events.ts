// Wails 事件名集中在这里：拼错事件名是「界面没反应」最常见的原因，
// 所以只允许从这里引用，不在组件里写字符串字面量。
export const PI_EVENT = 'pi:event'; // Go → 前端：类型化事件（agent.Envelope）
export const PI_TRACE = 'pi:trace'; // Go → 前端：原始 JSONL（协议观察器）
// Go 拦下"关闭窗口"时发来的通知：前端据此弹"还有回合在跑"的确认框
export const PI_CLOSE_REQUEST = 'pi:close-request';
// Go → 前端：从资源管理器拖进窗口的文件（原生投递，带真实路径）
export const PI_FILES_DROPPED = 'pi:files-dropped';
// Go → 前端：子智能体的状态通道（谁被派出、在跑什么、结果如何）
export const PI_AGENT_STATE = 'pi:agent-state';
// Go → 前端：排队中的会话切换**真正执行完**了（回合结束、循环不再续跑时才执行）。
// payload 与 load_session 的 Data 同构（对象，不是字符串）：
// 成功是 {cancelled:false, path, messages, state}，失败是 {path, error}。
// 有回合在跑时 LoadSession 只排队、不切换，界面靠这条事件才知道"可以切过去了"。
export const PI_SESSION = 'pi:session';
// 拖拽"悬停中"的状态由 DOM 的 dragenter/dragleave 推导（见 Composer）：
// Wails 的 WindowFileDraggingEntered 只有 macOS 有，Windows 走不通。

export { Events } from '@wailsio/runtime';
