# clipshare — 局域网剪贴板共享工具 · 设计文档

- 日期：2026-11-11
- 状态：已确认（待实现）
- 仓库：`/home/haitang/src/UserCode/tools`（module `clipshare`）

## 1. 背景与目标

同一局域网内多台设备（Ubuntu / Windows）工作的人，需要跨机器共享剪贴板内容与剪贴板历史。
现有工具要么依赖云服务，要么只做单次传送，要么在 Linux 上依赖一堆运行时。

目标：一个零依赖的单文件二进制，托盘常驻，自动同步文本剪贴板，并能浏览最近的剪贴板历史。

### 目标（v1）

1. 同房间节点之间，本机复制文本后，其他节点剪贴板自动变成同样内容（可关闭）。
2. 每台节点保留剪贴板历史（默认 500 条，落盘），并标出条目来源；可从历史里一键取回某条。
3. 局域网内零配置发现（mDNS），并为禁用多播的网络保留静态 IP 兜底。
4. 托盘图标常驻 + 菜单操作；托盘不可用时有降级路径。
5. Ubuntu（X11 / Wayland）与 Windows 双平台，同一份代码，`CGO_ENABLED=0` 交叉编译。

### 非目标（v1 明确不做）

- 图片 / 文件 / 富文本（只处理 `text/plain`）
- 跨网段、穿透 NAT、中继服务器
- 用户账号体系、多房间切换（一个进程一个房间）
- TLS 加密、重放攻击防护（房间密钥只解决「谁能接入」）
- 历史加密存储（明文落盘，可关闭持久化）
- 安装包 / 开机自启安装器（只提供文档说明）
- 移动端

## 2. 技术选型与已完成的可行性验证

| 需求 | 选型 | 验证结论 |
|---|---|---|
| 语言 | Go 1.24 | 本机 `go1.24.0 linux/amd64` |
| 托盘 | `github.com/energye/systray v1.0.3` | Linux 走 DBus StatusNotifierItem、免 cgo；Windows 走 Win32。实测 `CGO_ENABLED=0` 交叉编译 linux/amd64(5.1MB) 与 windows/amd64(4.2MB) 均通过 |
| 剪贴板 | `github.com/atotto/clipboard v0.1.4` | Linux 调 `xclip`/`xsel`/`wl-paste`，Windows 走 Win32；本机 `xsel` 已装 |
| 发现 | `github.com/grandcat/zeroconf v1.0.0` | 纯 Go mDNS，不依赖 Bonjour/avahi |
| 桌面托盘可用性 | — | 本机 `org.kde.StatusNotifierWatcher` 可达、`ubuntu-appindicators@ubuntu.com` 已启用 |

**已知平台依赖**：Linux 侧剪贴板读写依赖外部命令（`xclip` 或 `xsel`；Wayland 需 `wl-clipboard`）。启动时检测，缺失即明确报错，不静默空转。

## 3. 总览架构

对称 P2P：每个节点既是服务端也是客户端，无中心节点、无单点故障。两台机器即可工作。

```
┌──────────────── clipshare 进程 ────────────────┐
│                                                │
│  clip.Poller ──(变化)──► history.Store         │
│       ▲                     │                  │
│       │(写入)                │(新条目)          │
│       │                     ▼                  │
│  sync.apply            sync.Broadcaster ──HTTP POST──► peers
│       ▲                                        │
│       └──── sync.HTTPServer ◄──HTTP POST── peers
│                                                │
│  discovery (mDNS + seeds) ──► peers 集合        │
│  tray (菜单) ──► engine 方法                    │
│  webui (127.0.0.1) ──► history 只读 + 取回      │
└────────────────────────────────────────────────┘
```

### 组件边界与契约

每个组件只暴露一件事，互不直接依赖内部实现，全部可在单测里替换。

| 包 | 职责 | 对外契约 | 依赖 |
|---|---|---|---|
| `internal/config` | 加载/生成配置 | `Load(dir) (*Config, error)`、`(*Config).Save()` | 无 |
| `internal/clip` | 读写当前剪贴板、检测变化 | `type Clipboard interface { Read() (string, error); Write(string) error }`；`Poller{OnChange func(string)}` | 平台命令 |
| `internal/history` | 环形历史、去重、落盘 | `Add(Entry) bool`、`List(limit int) []Entry`、`Get(id) (Entry, bool)`、`Clear()` | config |
| `internal/sync` | HTTP 收发、房间鉴权、回环抑制 | `Server.Handler() http.Handler`、`Broadcast(Entry) []PeerResult`、`Client.Post(peer, Entry) error` | history、net/http |
| `internal/discovery` | 找出同房间 peers | `Peers() []Peer`、`Watch(func(peers []Peer))`、`Advertise(self)` | zeroconf |
| `internal/engine` | 组装上述组件、编排同步语义 | `Start() / Stop()`、`Pause(bool)`、`PullEntry(id) error`、`PushCurrent() error` | 以上全部 |
| `internal/tray` | 菜单与图标 | `Run(actions TrayActions)`，`actions` 为回调集合 | systray、engine |
| `internal/webui` | 本地历史页 | `Handler(store *history.Store, pull func(id string) error) http.Handler`，`go:embed` 单页 | history、engine 的取回回调 |

`engine` 是唯一知道「同步语义」的地方；托盘、Web UI、HTTP handler 都只是它的调用方。

## 4. 数据模型

```go
type Entry struct {
    ID       string    `json:"id"`        // hex(sha256(originNodeID + "\x00" + content))[:16]
    Origin   string    `json:"origin"`    // 产生该条目的节点 ID；空 = 未知
    OriginName string  `json:"originName"`
    Content  string    `json:"content"`   // text/plain，≤ ItemMaxBytes
    RecvAt   time.Time `json:"recvAt"`    // 本地接收/产生时间，排序只用它
}
```

- **排序与展示一律用 `RecvAt`**，不使用发送方时钟，规避跨机时钟漂移。
- `ID` 含 origin，因此不同机器上的相同内容是不同条目；跨机重复由内容 hash 的 seen 集处理。

```go
type Config struct {
    NodeName      string   `json:"nodeName"`      // 默认 os.Hostname()
    DeviceID      string   `json:"deviceId"`      // 首次启动 crypto/rand 生成
    RoomKey       string   `json:"roomKey"`       // 房间密钥；空则首启生成 6 位
    Port          int      `json:"port"`          // 默认 47100
    Seeds         []string `json:"seeds"`         // ["192.168.1.7:47100"]
    HistoryLimit  int      `json:"historyLimit"`  // 默认 500
    ItemMaxBytes  int      `json:"itemMaxBytes"`  // 默认 262144
    AutoApply     bool     `json:"autoApply"`     // 默认 true：自动写入本地剪贴板
    MDNS          bool     `json:"mdns"`          // 默认 true
    Persist       bool     `json:"persist"`       // 默认 true：历史明文落盘
    PollIntervalMs int     `json:"pollIntervalMs"` // 默认 400：剪贴板轮询间隔
}
```

配置路径：`os.UserConfigDir()/clipshare/config.json`（Linux `~/.config/clipshare/`，Windows `%APPDATA%\clipshare\`）。
历史文件：同目录 `history.json`，`WriteFile(tmp)` + `os.Rename` 原子替换。
CLI flag 与配置文件同时支持，**flag 优先级更高**（`--room`、`--port`、`--name`、`--seed`、`--no-tray`、`--no-persist`）。

## 5. 同步语义（行为规范）

核心不变量：
**任何被写入本地剪贴板的远端内容，都不得再被本地轮询误判为「用户复制」而回广播。**

实现方式：**不用 suppress 标志位，而是让轮询器直接把该内容记作「已见」**（`Poller.MarkSeen(content)` 改写内部 `lastSeen`）。
程序主动写入剪贴板后调用 `MarkSeen`，下一次轮询读到的内容与 `lastSeen` 相同，因此根本不会产生「变化」事件，也就无法回广播。

> 为什么不加 suppress 标志位：标志位需要「被下一次变化消费」，若这次写入恰好与 `lastSeen` 相同（轮询压根没触发），标志位就会滞留，
> 之后用户真的复制了同一段内容时被误吞。`MarkSeen` 无状态残留，不存在这个问题。

| # | 场景 | 行为 |
|---|---|---|
| S1 | 本机用户复制新内容 | 入历史（Origin=本机）+ 广播给所有 peers（暂停时不广播） |
| S2 | 收到远端条目 | 入历史（Origin=发送方）；若 `AutoApply` 且暂停关闭 → 写本地剪贴板 + `MarkSeen` |
| S3 | 历史条目被点击（取回） | 写本地剪贴板 + `MarkSeen`；**不广播** |
| S4 | 「发送当前剪贴板」 | 强制广播当前内容一次，绕过 `MarkSeen` 抑制（用于同步被误吞的确定性命中） |
| S5 | 暂停同步 | **不广播本机内容、不写本机剪贴板**；远端条目**仍会记录进本地历史**（历史与剪贴板是两条独立通道，与 `AutoApply` 关闭时的行为一致）；本地复制仍记入历史 |
| S6 | 收到重复内容（服务端 seen 命中） | 202 并丢弃，不写历史、不写剪贴板 |
| S7 | 广播失败 / 超时 3s | 记日志丢弃，**不重试** |
| S8 | 内容超过 `ItemMaxBytes` 或为空 | 跳过，不广播、不入历史，日志一行 |

**暂停的边界（明确说明，勿当缺陷）**：暂停是一个「外向」开关——它阻止本机内容被广播，并阻止远端内容覆盖本机剪贴板；但它**不阻止远端条目进入本地历史**，因为 `store.Add` 发生在 `sync.Server` 内、先于 engine 的 `apply` 回调，engine 在那一层无法拦截。
这是刻意选择：暂停最常用的场景是「我在处理敏感内容，别把我复制的东西发出去」，该场景已被正确覆盖；若要让暂停连历史也不记，需要给 `sync.Server` 增加门控（跨包改动），当前不做，需要时再评估。
**有意的取舍**：若用户在 B 上手打/复制了与 A 刚同步过来完全相同的内容，`lastSeen` 已等于该内容、不产生变化事件，因此不会广播。理由：内容已一致，广播无收益；代价是该条目不会在别的机器上刷新时间戳。此行为写进 README 的「已知行为」。

**刻意不做重试**：剪贴板内容有过期语义，延迟重发会造成「你以为复制了 A，剪贴板后来跳成 B」的时序错乱。

## 6. 协议与鉴权

明文 HTTP/1.1 + JSON，内网信任模型。

### `POST /v1/items`

请求头：

| 头 | 值 | 校验 |
|---|---|---|
| `Authorization` | `Bearer <roomKey>` | 不匹配 → 401 |
| `X-Room` | `sha256(roomKey)` 前 12 位 hex | 不匹配 → 403（防误配密钥串房间） |

请求体（单条）：

```json
{"id":"a1b2c3d4e5f60718","origin":"<deviceId>","originName":"MYPC","content":"hello","recvAt":"2026-11-11T18:40:00+08:00"}
```

响应：`202 Accepted`（含被丢弃的重复条目）；`400` 内容为空或超限；`401/403` 鉴权失败。
**服务端不接受客户端声明的 `recvAt` 用于排序**——收到后以服务端本地时间重写。

### `GET /v1/health`

响应 `{"nodeId":"…","name":"…","version":"0.1.0","roomHash":"…","clients":3}`，用于静态 seeds 探活与 room 校验。无需鉴权（代价：同网段可探测房间 hash 是否存在，但不泄露 roomKey 本身，接受）。

### 端口与超时

默认监听 `0.0.0.0:47100`；单次 POST 超时 3s；广播采用「每 peer 一个 goroutine + WaitGroup」，任一 peer 失败不影响其他。

### 安全边界（写进 README，不做代码防护）

- 组内任何人只要拿到 roomKey 即可读取同步流，也可以向他人的剪贴板写入内容。
- 明文传输：同网段可被嗅探/注入。
- v2 升级路径：在 `internal/sync` 的 listener/dialer 两处加自签 TLS，其余不变。

## 7. 发现机制

- mDNS 服务类型 `_clipshare._tcp`，TXT 记录：`room=<roomHash>/node=<deviceId>/name=<nodeName>/ver=1`
- 订阅端只接受 TXT 中 `room` 与自身一致的实例
- peers 按 `deviceId` 去重；mDNS 过期/TTL 出局即移除
- 静态 `Seeds`：每 30s 对每个 seed 请求 `/v1/health`，`roomHash` 匹配且 `nodeId` ≠ 自身才收编；mDNS 关闭时仍工作
- 无多播网络：`mdns:false` + 手填 seeds 是唯一可用路径，README 必须写清

## 8. 剪贴板适配

```go
type Clipboard interface {
    Read() (string, error)   // 空字符串 = 无文本（例如复制的是一张图）
    Write(s string) error
}
```

- 实现：基于 `atotto/clipboard` 的 `Adapter`；单测用 `fakeClipboard`
- 轮询间隔 400ms（可配 `pollIntervalMs`）。无变更事件 API，轮询是最小可行方案；每次轮询在 Linux 上会 fork 一次 `xsel/xclip`，实测可接受，升级路径为 v2 用 `xgb`+XFIXES 直接订阅
- 归一化：`\r\n` → `\n`，去除尾部 `\x00`，不 trim 内部空白（用户可能复制代码缩进）
- 空内容 / 非 UTF-8 / 超限 → 记为「无变化」，不产生条目
- 启动自检：`exec.LookPath` 检查 `xclip`/`xsel`（X11）或 `wl-paste`（Wayland），缺失则退出并打印安装命令

## 9. 托盘与 Web UI

### 托盘菜单

```
clipshare · <nodeName>          (禁用标题，房间码前 6 位)
与 3 台同步中 / 已暂停 / 无可用节点   (禁用状态行)
──────────
[本机] 前 40 字…                 ← 点击 = 取回本机剪贴板（S3）
[M-PC] 前 40 字…                 ← 最近 10 条（含来源标签、单行截断）
──────────
查看全部历史…                    → 打开 http://127.0.0.1:<uiPort>/ui
发送当前剪贴板                    ← S4
☑ 自动应用远端剪贴板               ← 切换 AutoApply
☑ 启用 mDNS 发现                 ← 切换 MDNS（重建 discovery）
──────────
打开配置目录
退出
```

图标三态：正常（有 peers）/ 暂停 / 无节点。
**菜单重建用增量 diff**（`AddMenuItem` / `RemoveMenuItem`），不允许定时全清重建（会闪）。若库能力不足，降级为「仅在条目集合变化时重建」并接受轻微闪烁——实现前先用 spike 验证并记录结论。

`--no-tray`：不初始化托盘，日志提示 Web UI 地址，功能其余不变（用于无 DBus / 无 GNOME 扩展的环境）。

### 本地历史页

- `GET /ui` 单页 HTML（`go:embed`，原生 JS，无构建步骤、无框架，目标 ≤ 250 行）
- **只绑 127.0.0.1**，不鉴权
- **UI 端口 = 同步端口 + 1**（默认 47100 → 47101），被占用时回退到系统分配端口并在启动日志里打印实际地址；端口规则不写进配置，避免与 `port` 两处漂移。UI 与同步服务是两个独立 listener，同步端口由 `engine` 持有、UI listener 由 `main` 持有（这样 `webui` 无需知道网络细节）。
- 列表：时间、来源标签、内容预览；行内「复制到本机」按钮（S3）；顶部搜索框（本地过滤）；「清空历史」需二次确认
- API：`GET /ui/api/entries`、`POST /ui/api/pull {id}`、`POST /ui/api/clear`

## 10. 错误处理与边界

| 情况 | 处理 |
|---|---|
| Linux 缺剪贴板命令 | 启动即报错退出，打印 `sudo apt install xclip` / `wl-clipboard` |
| 剪贴板内容为空（图片/二进制） | 视为无变化，跳过 |
| Windows `OpenClipboard` 被占用 | 重试 3 次（20/60/150ms 退避）后跳过本轮，不崩、不打错误日志洪流 |
| 端口被占用 | 报错退出，提示改 `port` |
| 托盘初始化失败 | 自动降级 `--no-tray` 模式并日志提示 |
| 配置文件损坏 | 备份为 `config.json.bak` 后用默认值重建，日志告警 |
| 历史文件损坏 | 忽略并从空历史起步，日志告警（不阻塞启动） |
| 落盘失败（磁盘满/权限） | 保留内存历史继续运行，日志节流告警（每分钟最多 1 条） |
| 跨机时钟漂移 | 排序只用本地 `RecvAt` |
| 自己发给自己（seed 里填了自己） | 按 `deviceId` 等于自身直接过滤 |

## 11. 测试策略

**单测（无外部依赖，`go test ./...` 必须全绿）**

- `history`：环形上限、内容去重的 seen 集、`RecvAt` 排序、损坏文件恢复、原子写
- `sync`：httptest 覆盖 202/400/401/403、seen 去重、批量、超时
- `engine`：**回环抑制状态机**（假剪贴板）—— S1/S2/S3/S4/S5/S8 逐条断言，重点覆盖 A→B→A 不产生回环风暴
- `config`：默认值生成、flag 覆盖、损坏恢复
- `discovery`：TXT 解析与 room 过滤（不真起 mDNS）
- `webui`：`/ui/api/entries` 返回结构与分页

**集成测（进程内）**：两个 engine 实例（各用假剪贴板 + 独立 HTTP 端口）互连，验证 A 复制 → B 剪贴板变化 → B 历史含 A 的条目 → B 不反向广播。

**手动验收（实现完成后必须做）**：本机真实 X11 剪贴板跑通；Windows 侧在同一局域网连通（或用本机两进程不同端口模拟）；mDNS 关闭 + 静态 seeds 模式跑通。

## 12. 里程碑与交付

| 里程碑 | 内容 | 完成判据 |
|---|---|---|
| M1 | config / clip / history / sync + 单测 | `go test ./...` 全绿，本机两进程可互发 |
| M2 | engine 编排 + discovery（mDNS + seeds） | 集成测通过，两机自动发现 |
| M3 | 托盘 + Web 历史页 | 托盘菜单可操作；`/ui` 可浏览与取回 |
| M4 | 双平台构建、README、手动验收 | `make build` 产出 linux/windows 二进制；验收清单全过 |

交付物：`cmd/clipshare/`、`internal/*`、`Makefile`（`build-linux`/`build-windows`/`test`）、`README.md`（安装、房间配置、多播禁用时的配法、安全边界、已知行为）。

版本号：`0.1.0`。

## 13. 风险与待验证项

| 风险 | 影响 | 缓解 |
|---|---|---|
| `energye/systray` 菜单增量更新能力不足 | 菜单重建闪烁 | M3 先写 spike 验证；不行则接受「仅集合变化时重建」 |
| 部分 Ubuntu 桌面未启用 AppIndicator 扩展 | 托盘图标不可见 | 启动检测 + 自动/手动 `--no-tray` 降级到 Web UI |
| Linux 剪贴板依赖外部命令 | 新机器装完不能直接跑 | 启动自检 + README 前置依赖表 |
| GNOME Wayland 下后台进程读写剪贴板 | 可能受 Wayland 安全模型限制 | `wl-paste` 通常可用；验收时实测，失败则 README 注明推荐 X11 会话 |
| 多网卡 / 虚拟网卡（docker0）多播异常 | mDNS 抖动 | peers 按 deviceId 去重 + seeds 兜底 |
