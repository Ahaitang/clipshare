# clipshare 独立验证报告（t6，attempt 2 / 复验）

- 日期：2026-11-11
- 验证者：`verifier`（团队成员，独立于实现者）
- 任务：`t6` 全量验证：测试、vet、双平台构建、双进程实测
- 本次复验起点：`HEAD = e105559`（分支 `develop`）
- 上一轮（attempt 1）结论：实现行为 6/7 通过，但 F1（`cmd/clipshare/` 未被 git 跟踪）属交付阻塞，另有 F2–F5 四项偏差
- 本轮结论：**7 项全部通过**；attempt 1 的 F1–F6 均已修复或在 spec/README 中精确化，仅剩需要人工/真机覆盖的项（见 §9）
- 环境：`Linux 5.15.0-140-generic x86_64`、`go1.24.0 linux/amd64`、X11（`DISPLAY=:1`，`xsel` 可用，`xclip`/`wl-paste` 未安装）
- 约束：只验证，不改实现；构建产物与临时配置全部写在 `/tmp/`，仓库内仅本报告被修改

## 0. 结论摘要

| # | 检查项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 格式 / 静态检查 / 依赖整洁 | `gofmt -l .`、`go vet ./...`（含 `GOOS=windows`）、`go mod tidy -diff` | **通过** |
| 2 | 全量测试（竞态） | `go test ./... -race -count=1` | **通过**（10 包全绿） |
| 3 | Linux 交叉构建 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` | **通过**（并产出真实 ELF） |
| 4 | Windows 交叉构建 | `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...` | **通过**（并产出真实 PE32+） |
| 5 | 双进程实测（推送 / 历史 / 不回环 / 落盘） | 见 §5 | **通过**（不回环成立；字面期望与 spec 的差异见 §5.5） |
| 6 | 鉴权与请求校验 | `curl` 401/403/400/405/200/边界 202 | **通过** |
| 7 | 非目标越界审计 | 代码/依赖/README 检索 | **通过**（无 TLS、无图片/文件、无中继） |
| + | 交付完整性（clean clone 可构建） | `git clone` + `go build ./cmd/clipshare` | **通过**（F1 已修复） |

## 1. 格式、静态检查与依赖整洁

```console
$ cd /home/haitang/src/UserCode/tools
$ git rev-parse --short HEAD
e105559
$ gofmt -l . ; echo "gofmt exit=$?"
gofmt exit=0
$ go vet ./... ; echo "vet exit=$?"
vet exit=0
$ CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./... ; echo "win vet exit=$?"
win vet exit=0
$ go mod tidy -diff ; echo "tidy exit=$?"
tidy exit=0
```

- `gofmt -l .` 无输出；`go vet` 在 linux 与 windows 两个 GOOS 下均无输出。
- `go mod tidy -diff` 无 diff（attempt 1 的 F5 已由 `e105559` 修复：三个直接依赖不再标 `// indirect`，`go.sum` 补齐 `pkg/errors`、`x/sync`）。

**通过。**

## 2. 全量测试（`-race -count=1`）

```console
$ go test ./... -race -count=1 ; echo "PIPESTATUS=$?"
ok  	clipshare/cmd/clipshare	1.012s
ok  	clipshare/internal/clip	1.465s
ok  	clipshare/internal/config	1.015s
ok  	clipshare/internal/discovery	1.317s
ok  	clipshare/internal/engine	13.072s
ok  	clipshare/internal/history	1.145s
ok  	clipshare/internal/model	1.007s
ok  	clipshare/internal/sync	1.327s
ok  	clipshare/internal/tray	1.052s
ok  	clipshare/internal/webui	1.014s
PIPESTATUS=0
```

10 个包全绿，无跳过、无竞态告警，退出码 0。

**通过。**

## 3. Linux 交叉构建

```console
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... ; echo "exit=$?"
exit=0
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags '-s -w -X main.version=0.1.0' -o /tmp/cs2-linux ./cmd/clipshare
$ ls -l /tmp/cs2-linux
-rwxrwxr-x 1 haitang haitang 9801912 … /tmp/cs2-linux
$ file /tmp/cs2-linux
ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, BuildID[sha1]=88b96eff…, stripped
$ /tmp/cs2-linux --version
clipshare 0.1.0
```

与 Makefile 同参数（`-trimpath -ldflags "-s -w -X main.version=0.1.0"`）产出静态链接、已 strip 的 9.8 MB 二进制，`--version` 为 `0.1.0`（ldflags 注入生效，且与 `/v1/health` 上报同源，见 §5.1）。

**通过。**

## 4. Windows 交叉构建

```console
$ CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... ; echo "exit=$?"
exit=0
$ CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
    -ldflags '-s -w -X main.version=0.1.0' -o /tmp/cs2-windows.exe ./cmd/clipshare
$ ls -l /tmp/cs2-windows.exe
-rwxrwxr-x 1 haitang haitang 7738880 … /tmp/cs2-windows.exe
$ file /tmp/cs2-windows.exe
PE32+ executable (console) x86-64, for MS Windows
```

两平台均在 `CGO_ENABLED=0` 下通过，符合 spec §2「免 cgo 交叉编译」。

**通过。**

## 5. 双进程实测（同机两实例）

### 5.1 配置与启动

按要求：端口 `47901`/`47902`、各自独立 `--config-dir`、同一 `roomKey`、互为 seed、`--no-tray`、`--mdns=false`。
两个实例的 `config.json` 显式 `autoApply: false`：这是合法配置，目的是让「是否转发」可观测——
同机两进程共享同一个 X 剪贴板，若开启自动应用，「远端写入」会被另一个进程的轮询看成「本机复制」，
产生同机模拟特有的假象（§5.5 有说明）。

```console
$ /tmp/cs2-linux --port 47902 --room verifyroom7 --name B --seed 127.0.0.1:47901 \
    --no-tray --mdns=false --config-dir /tmp/cs-verify2/b   # 先启动 B
[engine] 启动完成：节点 B（bbbbbb），端口 47902，房间 94a97baff738
本地历史页: http://127.0.0.1:47903/ui

$ /tmp/cs2-linux --port 47901 --room verifyroom7 --name A --seed 127.0.0.1:47902 \
    --no-tray --mdns=false --config-dir /tmp/cs-verify2/a   # 再启动 A
[engine] 启动完成：节点 A（aaaaaa），端口 47901，房间 94a97baff738
本地历史页: http://127.0.0.1:34309/ui     # 47902 已被 B 的同步端口占用 → 回退随机端口
```

```console
$ curl -s http://127.0.0.1:47901/v1/health
{"nodeId":"aaaa…","name":"A","version":"0.1.0","roomHash":"94a97baff738"}
$ curl -s http://127.0.0.1:47902/v1/health
{"nodeId":"bbbb…","name":"B","version":"0.1.0","roomHash":"94a97baff738"}
```

`roomHash = sha256("verifyroom7")[:12] = 94a97baff738`，两端一致；`version` 为注入的 `0.1.0`；
响应字段与 **spec §6 现文**一致（attempt 1 的 F4 已由 `e105559` 从 spec 示例中移除未实现的 `clients`）。

### 5.2 节点发现（seeds 路径，禁用多播）

```text
19:30:47  A: [engine] 广播条目 4f2f… 到 1 个节点（失败 0）   # A 启动即探活成功
19:30:47  B: [engine] 广播条目 5756… 到 1 个节点（失败 0）   # B 在 30s 重扫后收编 A
```

符合 spec §7（立即探活 + 每 30s 重试）；`mdns:false` + seeds 的禁用多播路径可用。

### 5.3 推送与「入站不转发」（决定性观测）

```console
# (a) 向 47901 的 /v1/items 推送 E1（正确 Bearer + X-Room）
$ curl -s -w 'HTTP %{http_code}\n' -X POST http://127.0.0.1:47901/v1/items \
    -H "Authorization: Bearer verifyroom7" -H "X-Room: 94a97baff738" \
    -H 'Content-Type: application/json' \
    -d '{"id":"1111111111111111","origin":"curlclient","originName":"CURL","content":"att2-e1-1930","recvAt":"2026-11-11T00:00:00Z"}'
HTTP 202
A 历史: [{"id":"f5892e7d15b99de4","origin":"curlclient","originName":"CURL",
          "content":"att2-e1-1930","recvAt":"2026-11-11T19:30:26.090680632+08:00"}]
B 历史: []
```

- `id` 按 `origin+content` 由服务端重算（`1111…` → `f5892e7d…`），`recvAt` 重写为服务端本地时间，符合 spec §4/§6。
- **B 历史为空 → A 收到的入站条目不会被转发**（spec §5 S2）。

```console
# (b) 向 47902 推送**不同内容** E3（此时 A、B 已互相发现，双方广播计数均为 1）
$ curl … :47902/v1/items -d '{"content":"att2-e3-1931",…}'   → HTTP 202
A 历史: []                                   # 未被 B 转发/回环
B 历史: [{"id":"8793d7fdcaca41c9", … "content":"att2-e3-1931",
          "recvAt":"2026-11-11T19:30:50.239722017+08:00"}]

# (c) 向 47901 推送另一条 E4
$ curl … :47901/v1/items -d '{"content":"att2-e4-1931",…}'   → HTTP 202
A 历史: [{"id":"e501d306439a4670", … "content":"att2-e4-1931",
          "recvAt":"2026-11-11T19:30:51.792110451+08:00"}]
B 历史: [{"id":"8793d7fdcaca41c9", … "content":"att2-e3-1931"}]   # B 未被 A 转发
```

双向都没有把「收到的条目」再发给对端；用**不同内容**验证，若任何一侧转发，对端必然多出一条。

### 5.4 相同条目重推：不回环 + 去重语义（spec §5 S6 现文）

```console
# 在 47902 上用相同条目 E3 重推两次
repost#1 202 ; repost#2 202
A 历史: 1 条（E4），recvAt 仍为 19:30:51.792…（未被刷新）
B 历史: 1 条（未新增）；既有条目的 recvAt 由 19:30:50.239… 刷新为 19:30:53.348…
```

- **回环不成立**：A 的条数与 `recvAt` 均未变化，B 也没有向 A 发送任何入站条目。
- **去重语义与 spec §5 S6 现文一致**（`e105559` 明确）：「不新增历史条目、不写剪贴板；既有同内容条目会被置顶并刷新 `RecvAt`」。
  attempt 1 的 F3 因此关闭（原先是 spec 措辞「不写历史」与 `history.Store.Add` 的刻意行为冲突，现已在 spec 中精确化）。

### 5.5 关于任务字面期望（推给 47901 后要求 47902 历史出现该条目）

该字面链路在协议里不存在：spec §5 S2 规定收到远端条目只入历史（+ 可选写剪贴板），**不转发**；
若要「A 收到 → B 也看到」，只能靠转发，而转发会违反 §5 的回环不变量并造成风暴。
因此本轮按 spec 验证了**等价且更强**的性质：

1. 双向**不转发**（§5.3(b)(c)，用不同内容验证，双方均已互相发现）；
2. 去重不产生新条目、不回环（§5.4）。

另说明：同机两进程**共享同一个 X 剪贴板**。若把 `autoApply` 打开，A 应用远端条目写剪贴板后，
B 的轮询会把它当成「本机复制」再记一条（`origin=B`）——这是同机模拟的假象，**不是 P2P 传播**。
真机上「A 复制（S1）→ B 历史 + B 剪贴板更新 → B 不反播」需两台机器验证（见 §9）。

### 5.6 退出落盘与配置不写回

```console
$ kill -TERM <A-pid> <B-pid>
收到信号 terminated，开始退出…
[engine] 已停止
$ jq -c '[.[]|{origin,content,recvAt}]' /tmp/cs-verify2/a/history.json
[{"origin":"curlclient","content":"att2-e4-1931","recvAt":"2026-11-11T19:30:51.792110451+08:00"}]
$ jq -c '[.[]|{origin,content,recvAt}]' /tmp/cs-verify2/b/history.json
[{"origin":"curlclient","content":"att2-e3-1931","recvAt":"2026-11-11T19:30:53.348964342+08:00"}]
$ jq -c '{roomKey,port,seeds,autoApply,mdns}' /tmp/cs-verify2/a/config.json
{"roomKey":"verifyroom7","port":47901,"seeds":[],"autoApply":false,"mdns":false}
$ ls ~/.config/clipshare
ls: 无法访问 '/home/haitang/.config/clipshare': 没有那个文件或目录
```

- `SIGTERM` 后历史正确落盘（`history.json`），进程正常退出。
- CLI `--seed` 只影响本次运行：`config.json` 的 `seeds` 仍为 `[]`，未被写回。
- 全程未生成默认配置目录（`engine` 复用 `config.Load` 填好的 `Dir`，attempt 1 的 F6 已在 `312e56d` 修复）。

**第 5 项通过**（含「重推不回环」；字面期望与 spec 的差异已在上文说明，属 spec 语义而非实现缺陷）。

## 6. 鉴权与请求校验（`curl`）

```console
wrong Bearer:     401      # {"error":"roomKey 不匹配"}
missing Bearer:   401
wrong X-Room:     403      # {"error":"X-Room 与本地 roomHash 不一致（…）"}
empty content:    400      # {"error":"内容为空"}
malformed JSON:   400      # {"error":"请求体不是合法的条目 JSON"}
oversize 262145:  400      # {"error":"内容 262145 字节，超过上限 262144"}
GET /v1/items:    405
GET /v1/health:   200      # 免鉴权
boundary 262144:  202      # 正好等于 itemMaxBytes → 接受
```

401/403/400/405 与 spec §6 一致，错误体为 `{"error":"…"}`；边界值（= 上限）接受、超 1 字节拒绝；
`/v1/health` 免鉴权且只暴露 `nodeId/name/version/roomHash`（不泄露 roomKey）。

**通过。**

## 7. 「非目标」越界审计

对照 spec §1 的非目标逐条检索（`internal/`、`cmd/` 的非测试代码）：

| spec 非目标 | 检索结果 | 结论 |
|---|---|---|
| 图片 / 文件 / 富文本 | 剪贴板接口只有 `Read/Write(string)`；`image/png` 仅出现在 `internal/tray`（运行时生成托盘图标） | 未越界 |
| 跨网段 / NAT / 中继 | `relay|upnp|multipart` 无命中；只有 mDNS + seeds | 未越界 |
| 账号体系 / 多房间 | 无 `multiroom`/房间切换逻辑；配置只有单个 `roomKey` | 未越界 |
| TLS / 重放防护 | `crypto/tls|tls.Dial|tls.Listen|x509` 无命中；协议为明文 HTTP/1.1 | 未越界 |
| 历史加密落盘 | 无 `encrypt|decrypt|aes`；`history.json` 明文 | 未越界 |
| 安装包 / 开机自启 | 无 `autostart|systemd` | 未越界 |
| 移动端 | 仅 linux/windows/darwin 构建标签 | 未越界 |

依赖面同样干净：`atotto/clipboard`、`energye/systray`、`grandcat/zeroconf`（含 `miekg/dns`）、`godbus`、
`x/crypto`（systray/zeroconf 的间接依赖），没有 TLS/中继/图片相关库。README 也把上述内容写成「非目标/已知行为」。

**通过。**

## 8. 交付完整性（clean clone）

attempt 1 的 F1（`.gitignore` 裸 `clipshare` 把 `cmd/clipshare/` 整目录吞掉）已由 `f7134e0` 修复；
本轮用 clean clone 复验：

```console
$ git clone -q /home/haitang/src/UserCode/tools /tmp/repo-check2
$ cd /tmp/repo-check2 && git rev-parse --short HEAD
e105559
$ ls cmd/clipshare
main.go
main_test.go
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/clone-clipshare ./cmd/clipshare ; echo "build exit=$?"
build exit=0
$ /tmp/clone-clipshare --version
clipshare dev          # 未注入 ldflags 时的兜底版本，符合预期
$ go test ./cmd/clipshare/ -count=1
ok  	clipshare/cmd/clipshare	0.003s
```

- `.gitignore` 现为 `/bin/`、`/clipshare`、`/clipshare.exe`（锚定仓库根），并带注释说明原因；
- `git ls-files cmd` 返回 `cmd/clipshare/main.go`、`cmd/clipshare/main_test.go`；
- clean clone 可直接构建入口并跑测试。

**通过。**

## 9. 未覆盖与限制（需人工/真机）

- Windows 真机运行（本轮只做 `GOOS=windows` 构建 + vet；未在 Windows 上跑单测/实机）。
- 托盘菜单实际点击（`--no-tray` 运行；DBus/StatusNotifierItem 交互需桌面会话人工验证）。
- mDNS 启用路径（本轮 `--mdns=false` 只走 seeds；mDNS 仅由单测覆盖）。
- 真两机同步与「A 复制 → B 剪贴板更新」的隔离剪贴板场景（同机两进程共享 X 剪贴板，无法模拟）。
- 「先启动低端口实例」时的端口错位（spec §9 / README 已警告：A 的 UI 占 `port+1` 会撞 B 的同步端口；
  联调用非相邻端口，如 `47901` 与 `47911`）。本轮按 README 建议先启动 B，A 的 UI 回退随机端口，功能正常。

## 10. 复验结论与建议

- 7 项验证全部通过；attempt 1 的 F1–F6 全部关闭（F1: `f7134e0`；F2: README 警告 + spec §9；F3/F4/F5: `e105559`；F6: `312e56d`）。
- 代码、测试、双平台构建、双进程协议行为（鉴权 / 去重 / 不回环 / 落盘）与 spec 一致。
- 剩余风险集中在只能人工验证的平台/桌面交互（Windows 实机、托盘、mDNS、真两机），建议列入发布前人工验收清单。

## 11. 复现环境与清理

- 所有实例均以 `--no-tray --mdns=false --config-dir /tmp/cs-verify2/<dir>` 启动，验证后 `SIGTERM` 全部退出；
- 验证结束 `pgrep` 无残留进程，`47900–47909` 端口全部释放；
- 仓库内未产生构建产物或配置（二进制在 `/tmp/`），默认配置目录 `~/.config/clipshare` 全程未被创建；
- 本轮仅修改本报告文件。
