# clipshare 跨局域网中继（hub / relay）设计文档

- 日期：2026-11-14
- 状态：**P1/P2 已实现并真机验证**（2026-11-14）；P3 的打包与图文手册待补
- 关联：[局域网版设计](2026-11-11-lan-clipboard-design.md)、[任务拆解](2026-11-14-cross-lan-relay-tasks.md)

## 1. 背景与目标

现有版本是对等 P2P：同网段内靠 mDNS / 静态 seeds 互相发现，直接互推。B、C 分处不同网络（各自在 NAT 后）
时无法直连，因此需要一个可达的中心点 A 做中继。

### 目标

1. **同一个二进制、同一份配置**支持两种拓扑，靠配置切换：
   - **① 局域网 P2P**：现有行为，一字不改（`lanEnabled: true`）
   - **② 借助中心点 A**：A 开中继服务端角色（`hubEnabled`），B/C 配 `relayURL` 指向 A
2. 两种拓扑**可以同时启用**（A 一边与本地机器 P2P，一边为远端 B/C 中继；B 一边连公司 LAN，一边连 A）。
3. A 侧保留每个房间最近 `hubKeep`（默认 200）条，供断线重连的 B/C 补收。
4. 公网可用的最低安全线：TLS（自签 + 指纹钉，非 80/443 端口）+ 现有房间密钥鉴权。
5. 跨网络后剪贴板仍表现为「复制即到」，可接受延迟 1 秒量级（长轮询实现）。

### 非目标（本次明确不做）

- **端到端加密**：中继节点 A 能读到内容。这是用户明确接受的信任边界（A 是自有机器）。
- 用户账号体系、多租户、计费、Web 控制台。
- 图片 / 文件同步（仍只同步纯文本）。
- 服务端全文搜索与归档（A 只保留环形窗口，不做长期归档页）。
- 移动端直连中继（Android 发送端仍只走局域网 `POST`；接 A 留待以后）。

## 2. 角色模型（不用「模式」，用三个独立开关）

| 角色 | 配置 | 谁用 | 关键行为 |
|---|---|---|---|
| LAN 对等 | `lanEnabled`（默认 **true**） | 同网段的机器 | 现有行为：mDNS/seeds 发现 + 直连互推 + `/v1/items` 接收 |
| 中继服务端 | `hubEnabled` + `hubListen` | A 服务器 | 收下房间内上行条目 → 环形队列 → 下发给订阅者；不需要剪贴板、不需要托盘 |
| 中继客户端 | `relayURL` 非空 | B / C | 本机变化上行到 A；从 A 拉取其它节点的条目并应用 |

组合合法性：

- 三者全关 → **启动失败**并明确报错（避免「跑着但什么都不做」）。
- `hubEnabled` 且 `hubListen` 非回环、又没有证书 → 拒绝启动，除非 `hubAllowPlaintext: true`（仅内网测试，启动日志大声警告）。
- A 同时开 `lanEnabled` + `hubEnabled` 是**合法且推荐**的默认：
  本地机器走 P2P，远端走中继，两条链路互不干扰。

## 3. 必须修正的语义：S2「不转发」只约束 LAN 对等角色

现有不变量是「收到远端条目只入历史 + 写剪贴板，绝不二次广播」，这是 A→B→A 不回环的根基
（见局域网版 §5、集成测 `TestIntegrationNoLoopback`）。

**中继服务端的本质就是转发**，因此该不变量按角色分域：

| 角色 | 是否转发 | 防回环机制 |
|---|---|---|
| LAN 对等 | **不转发**（不变） | 收到即 apply + `MarkSeen`，轮询不再把同一内容当本机复制 |
| 中继服务端 A | **按设计转发** | 只把「上行提交」入队并下发；不把下发动作再当作新条目 |
| 中继客户端 B/C | 不转发 | 收到即 apply + `MarkSeen`；**忽略 `origin == 自己 deviceId` 的条目**（防自己刚发的被自己应用） |

局域网版 spec 的 S2 条目需补充一句范围限定，避免后来者把中继的转发当成回归缺陷。

## 4. 协议（中继通道）

复用现有 Entry 与鉴权，不复用「不能下发」这条约束。

### `POST /v1/items`（上行，与 LAN 版同端点同 body）

- 头：`Authorization: Bearer <roomKey>`（TLS 之上）、`X-Room: sha256(roomKey)[:12]`
- body：与 LAN 版一致的 Entry JSON（客户端带 `origin=deviceId`、`originName=节点名`）
- 响应：`202 {"seq": <房间内单调递增序号>}`；鉴权失败 401/403；超限 400

### `GET /v1/feed?since=<seq>&wait=<ms>`（下行，长轮询）

- `since` 省略时**只返回当前游标**，不回放历史（新客户端不该被灌一整队历史）
- `wait` 上限 5000ms：有新条目立即返回，否则最多挂起到超时再返回空列表（省请求、降延迟）
- 响应：

```json
{"items":[Entry...],"lastSeq":42,"peers":3}
```

### `GET /v1/health`（扩展，免鉴权）

在现有字段基础上加 `"role":"relay"` 与 `"peers":N`，便于 B/C 与人工排障确认 A 是否在线。

### 序号与环形窗口

- 每个房间一个 `uint64` 单调序号，条目入队即分配；窗口保留最近 `hubKeep` 条。
- 客户端记录 `lastSeq`（内存即可）：重连后从上次游标继续；若游标已滑出窗口，
  A 返回当前 `lastSeq` 且 `items` 为空（客户端跳到最新，不试图补远古历史）——**宁可漏补，不做重放风暴**。

## 5. 配置（扁平字段，向后兼容）

```json
{
  "lanEnabled": true,
  "port": 47100,
  "mdns": true,
  "seeds": ["192.168.1.7:47100"],

  "hubEnabled": false,
  "hubListen": ":8443",
  "hubKeep": 200,
  "hubCertFile": "",
  "hubKeyFile": "",
  "hubAllowPlaintext": false,

  "relayURL": "https://a.example.com:8443",
  "relayPin": "sha256:3f9c…",
  "relayIntervalMs": 1000
}
```

- **向后兼容**：旧 `config.json` 没有这些字段时，默认值等价于「纯 LAN 模式」，行为与今天完全一致（需测试锁定）。
- 端口选择：A 用**非 80/443**（默认 8443），避免与既有 Web 服务冲突、也避开部分企业网关对 80/443 的审计。
- `hubKeep` 范围 0..10000（0 = 纯转发不落窗口，客户端只能拿到「从现在起」的内容）。

## 6. TLS 与鉴权

- **证书**：优先读取 `hubCertFile`/`hubKeyFile`；都没有时**首次启动自动生成自签证书**
  （写入配置目录 `hub-cert.pem` / `hub-key.pem`，SAN 覆盖本机 IP 与主机名，有效期 10 年），
  并在日志与 `clipshare hub-info` 输出里打印 **SHA-256 指纹**。
- **指纹钉**：`relayPin` 形如 `sha256:<64 hex>`；TLS 握手时比对证书指纹，不匹配即断开（不依赖 CA 体系）。
- **房间密钥**：沿用现有 `roomKey`。公网暴露下建议 ≥16 位（现有默认 12 位 ≈59 bit 仍可用，文档提示可加长）。
- **限流**（最小必要）：请求体上限沿用 `itemMaxBytes`；每房间条目速率上限（默认 60 条/分钟，
  超限返回 429 并记录）；`/v1/feed` 单连接最长 5 秒由服务端兜底，防挂死连接堆积。
- **明确记录的信任边界**：A 能读到所有经过它的剪贴板内容；明文不落盘由 `hubKeep`/`persist` 控制。

## 7. 引擎改造（多 transport 扇出）

现状：`engine.Start()` 里硬接 `sync.Server` + `sync.Broadcaster` + `discovery.Finder`；
`engine.broadcast(entry)` 只走一个广播器。

改造目标（保持 LAN 行为逐字不变）：

```go
// engine 内部：多路扇出
type transport interface {
    Name() string
    Start() error
    Stop()
    Publish(model.Entry) error // 上行：本机产生的条目
}
```

- `lanTransport`：把现有 `discovery.Finder + sync.Server + sync.Broadcaster` 包起来，
  行为与今天完全一致（现有 13 个包的测试作为回归网）。
- `hubTransport`：服务端角色，复用 `model.Entry` 与房间鉴权，新增 §4 的两个端点。
- `relayTransport`：客户端角色，上行 `POST`、下行长轮询，收到条目后交给 engine 的统一 apply 路径
  （与 LAN 的 apply 共用 `store.Add` + `applyLocal` + `MarkSeen`）。
- `engine.broadcast` → 遍历 `transports` 逐个 `Publish`；错误只记日志（沿用 S7 不重试）。

## 8. 错误处理与边界

| 情况 | 处理 |
|---|---|
| A 不可达 / TLS 指纹不匹配 | 客户端记日志并指数退避重连（1s → 30s 上限）；本机历史与 LAN 同步不受影响 |
| A 重启（序号归零） | 客户端发现 `lastSeq` 回退 → 重置游标为 A 当前值，不报错 |
| 房间密钥不一致 | 401/403，客户端日志明确「与中继房间不一致」 |
| 客户端时钟/序号异常 | 排序一律用本地 `recvAt`；序号只用于增量拉取 |
| 中继队列溢出 | 只保留窗口内最近 `hubKeep` 条；客户端游标过旧时跳到最新（§4） |
| A 自身也要用剪贴板 | 同时开 `lanEnabled` 正常使用；hub 角色不依赖剪贴板 |

## 9. 测试策略

- **单元**：序号分配、环形窗口裁剪、`since` 语义（含游标过旧 → 跳最新）、长轮询超时、
  指纹校验（自签证书生成 → 指纹稳定）、配置兼容（旧 config 加载后等价于纯 LAN）。
- **集成**：进程内起 A（hub，`127.0.0.1:随机端口`）+ B/C（relay），验证：
  B 复制 → C 剪贴板收到；C 复制 → B 收到；**B 不会收到自己发的条目**；A 重启后恢复同步；
  A 保留窗口后 C 断线重连能补收。
- **回归**：现有 LAN 集成测与 `TestIntegrationNoLoopback` 必须保持全绿（证明改造没动坏）。
- **真机**：A 用真实服务器（TLS + 非 443 端口），B/C 分别在不同网络（可用手机热点 + 办公网）互发。

## 10. 里程碑

| 阶段 | 内容 | 完成判据 |
|---|---|---|
| P1 | transport 抽象（纯重构，无新功能） | LAN 行为不变，全部现有测试 + 集成测全绿 |
| P2 | hub 服务端 + relay 客户端 + TLS/指纹 + 长轮询 | 进程内三节点集成测通过；A 重启、断线重连场景通过 |
| P3 | 交付：hub 的 systemd unit / docker 跑法、`clipshare hub-info`、邀请码扩展（可选）、README 与手册新版、真机三地验收 | 三地真实互发成功；安装包与文档重出 |

## 11. 风险

| 风险 | 缓解 |
|---|---|
| 中继回环（B 收到自己发的、或 A 与 B 互推） | 客户端忽略 `origin==self`；apply 后 `MarkSeen`；集成测覆盖 |
| 长轮询被中间代理缓冲 | `wait` 上限 5s；文档写明需要允许长连接；失效则退化为 1s 轮询 |
| 自签指纹变了导致客户端连不上 | 指纹变更时日志给出「新指纹 + 已钉指纹」对照，并提示如何更新 `relayPin` |
| A 成为单点 | 明确定位是「小团队中继」；不做高可用；客户端在 A 不可用时自动退回纯 LAN |
