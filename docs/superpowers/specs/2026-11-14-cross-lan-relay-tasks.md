# 跨局域网中继 任务拆解

依据：[跨局域网中继设计](2026-11-14-cross-lan-relay-design.md)。
原则：**每一期不过测试就不动下一期**；P1 是纯重构，必须证明局域网行为一字未变。

## P1 — 抽象（纯重构，无新功能）

- **T1 transport 抽象**：`internal/engine` 内引入 `transport` 接口（`Name/Start/Stop/Publish`），
  把现有 `discovery.Finder + sync.Server + sync.Broadcaster` 包装成 `lanTransport`；
  `engine.broadcast` 改为遍历 transports 逐个 `Publish`
  - 验证：`go test ./... -race` 全绿；本机两进程 `--port` 互指仍能双向同步（真机实测）
  - 关键约束：**不得**改动 `internal/sync` 与 `internal/discovery` 的对外行为

- **T2 配置扩展**：新增 `lanEnabled` / `hubEnabled` / `hubListen` / `hubKeep` /
  `hubCertFile` / `hubKeyFile` / `hubAllowPlaintext` / `relayURL` / `relayPin` / `relayIntervalMs`
  - 默认值等价于纯 LAN；旧 `config.json` 加载后行为与今天一致
  - 校验：三者全关 → 启动报错；`hubEnabled` + 非回环监听 + 无证书且未显式 `hubAllowPlaintext` → 拒绝启动
  - 验证：单测覆盖「旧配置兼容」「全关报错」「非回环无证书拒绝」「hubKeep 越界」

## P2 — 中继实现

- **T3 hub 服务端**（`internal/hub`）：房间注册表、`uint64` 序号、环形窗口（`hubKeep`）、
  两个端点（`POST /v1/items` 带 seq 响应、`GET /v1/feed?since&wait` 长轮询）、
  鉴权（Bearer + X-Room）、`itemMaxBytes` 上限、每房间速率上限（超限 429）
  - 验证：`httptest` 覆盖 202+seq、401/403/400/429、`since` 省略只回游标、游标过旧跳最新、
    `wait` 超时返回空、环形裁剪

- **T4 TLS 与指纹**：自签证书自动生成（SAN 覆盖本机 IP/主机名，10 年）、
  指纹打印、`relayPin` 校验（不匹配立即断开并给出新旧指纹对照）
  - 验证：单测生成证书 → 指纹稳定；客户端 pin 正确可连、错误必断；无证书时拒绝启动（见 T2）

- **T5 relay 客户端**（`internal/relay`）：上行 `POST`、下行长轮询（`relayIntervalMs` 作为 wait）、
  指数退避重连（1s→30s）、`lastSeq` 只在内存、忽略 `origin == 自己 deviceId`
  - 验证：集成测（进程内 A+B+C，三个随机 loopback 端口）：
    B 复制 → C 收到；C 复制 → B 收到；**B 不收到自己发的**；A 重启后恢复；
    C 断线期间条目在窗口内 → 重连补收；游标滑出窗口 → 跳到最新且不报错

- **T6 同时启用**：B 同时开 `lanEnabled`（一个 LAN 假 peer）与 `relayURL`（A）
  - 验证：测试断言两条链路都收到、且不重复应用（内容去重后历史只有一条）

## P3 — 交付

- **T7 运维与文档**：hub 的 systemd unit 模板、docker 运行示例、`clipshare hub-info`
  （打印监听地址/证书指纹/房间/在线客户端数）、README 与图文手册新增「跨网络」章节
  - 验证：README 每条命令实跑；`hub-info` 输出与日志一致

- **T8 打包与真机**：`make dist` 增加 hub 相关文件；三地真机验收
  （A 用真实服务器 + 自签证书 + 非 443 端口，B/C 分别在不同网络，互发中文与长文本）
  - 验证：三地互发成功；A 重启后 B/C 自动恢复；`relayPin` 错误时能明确报错

## 依赖顺序

```
T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8
```

T3 与 T4 可并行（前者纯 HTTP 语义，后者只碰 TLS 层）。
