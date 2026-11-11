# clipshare 任务拆解

依据：[设计文档](2026-11-11-lan-clipboard-design.md)。每个任务必须有可执行的验证方式，验证不过不算完成。

## M1 — 核心同机可跑

- **T1 脚手架**：`go.mod`（module `clipshare`）、`cmd/clipshare/main.go`（只解析 flag 并打印版本）、`Makefile`（`build-linux`/`build-windows`/`test`/`vet`）
  - 验证：`go build ./... && go vet ./...` 通过；`make build-linux` 产出 `bin/clipshare`
- **T2 `internal/model`**：`Entry`、`NewEntry`、`ContentHash`、`RoomHash`、`Normalize`
  - 验证：单测覆盖 ID 稳定性、hash 长度、`\r\n` 归一化
- **T3 `internal/config`**：默认值、JSON 读写、flag 覆盖、损坏文件备份重建、`DeviceID`/`RoomKey` 首启生成
  - 验证：`config_test.go` 覆盖 5 个场景；损坏文件场景断言生成 `.bak`
- **T4 `internal/history`**：环形上限、内容重复→置顶并刷新 `RecvAt`、原子落盘、损坏文件从空历史恢复、落盘失败保留内存
  - 验证：`history_test.go`；`-race` 通过
- **T5 `internal/clip`**：`Clipboard` 接口、`atotto` 适配器、`Poller`（400ms、`lastSeen` 变更检测、`MarkSeen` 用于抑制自身写入）、Linux 依赖自检
  - 验证：`poller_test.go` 用假剪贴板断言「自身写入不触发 OnChange」；`lookpath` 自检单测
- **T6 `internal/sync`**：`Server.Handler`（202/400/401/403）、`/v1/health`、`Client.Post`（3s 超时）、`Broadcaster`（每 peer 一 goroutine，失败不影响其他）
  - 验证：`httptest` 覆盖全部状态码与 seen 去重；广播失败场景断言不 panic

## M2 — 编排与发现

- **T7 `internal/engine`**：组装组件，实现 S1–S8；`Start/Stop`、`Pause`、`PullEntry`、`PushCurrent`、`ApplyRemote`、`Snapshot`
  - 验证：进程内两个 engine + 假剪贴板 + 真实 HTTP 端口，断言 A→B 同步、**A→B→A 不回环**、暂停生效、取回不广播
- **T8 `internal/discovery`**：mDNS 公告/订阅 + TXT 解析与 room 过滤 + 静态 seeds 30s 探活 + 按 `deviceId` 去重、排除自身
  - 验证：TXT 解析与过滤为纯函数单测；seeds 探活用 httptest 桩
- **T9 集成测**：完整链路 `go test ./... -race`
  - 验证：全绿

## M3 — 界面

- **T10 托盘 spike + 实现**：先验证 `energye/systray` 的菜单增删能力，再实现三态图标、状态行、最近 10 条、勾选项、退出；`--no-tray` 降级
  - 验证：本机 GNOME 下托盘图标出现且菜单可操作（人工）；`--no-tray` 启动不报错
- **T11 `internal/webui`**：`go:embed` 单页 + `/ui/api/entries|pull|clear`，只绑 127.0.0.1
  - 验证：`webui_test.go` 断言三个 API；浏览器实测可搜索、可复制、可清空

## M4 — 交付

- **T12 构建与文档**：Makefile 双平台构建、`README.md`（前置依赖、房间配置、禁多播配法、安全边界、已知行为、排障）
  - 验证：`make build-linux build-windows` 产出两平台二进制；README 里的每条命令实跑一遍
- **T13 手动验收**：本机真 X11 剪贴板跑通；两进程不同端口模拟双机；`mdns:false` + seeds 模式跑通
  - 验证：验收清单逐项勾选，结果写入 README「验收记录」

## 执行顺序与依赖

```
T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → T9 → T10 → T11 → T12 → T13
```

T2/T3 无相互依赖，可与 T4/T5 交叉推进；T10 spike 若发现菜单能力不足，先回改 spec 的 §9 再继续。
