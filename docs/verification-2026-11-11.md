# clipshare 独立验证报告（t6）

- 日期：2026-11-11
- 验证者：`verifier`（团队成员，独立于实现者）
- 任务：`t6` 全量验证：测试、vet、双平台构建、双进程实测
- 验证起点：`HEAD = 312e56d`（分支 `develop`，工作区干净）
- 环境：`Linux 5.15.0-140-generic x86_64`、`go1.24.0 linux/amd64`、X11（`DISPLAY=:1`，`xsel` 可用，`xclip`/`wl-paste` 未安装）
- 约束：只验证，不改实现；所有构建产物与临时配置写在 `/tmp/cs-verify/`，未触碰仓库文件（除本报告）

> 说明：验证过程中其它成员先后提交了 `0d74bac`、`ba181c1`、`312e56d`（19:19–19:21）。
> 下文的静态检查、构建与双进程实测**全部在 `312e56d` 上重跑过**；唯一例外是第 5 节里
> 「默认 `autoApply=true` 的 S2 应用行为」那一次观察（19:18，当时 `engine` 与 HEAD 仅差
> `--config-dir` 修复，不影响同步语义），已在文中标注。

## 0. 结论摘要

| # | 检查项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 格式 / 静态检查 | `gofmt -l .`、`go vet ./...`（含 `GOOS=windows`） | **通过** |
| 2 | 全量测试（竞态） | `go test ./... -race -count=1` | **通过**（10 包全绿） |
| 3 | Linux 交叉构建 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` | **通过**（并产出真实 ELF） |
| 4 | Windows 交叉构建 | `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...` | **通过**（并产出真实 PE32+） |
| 5 | 双进程实测（推送 / 历史 / 不回环） | 见 §5 | **部分不成立**：不回环成立；「B 历史出现推给 A 的条目」按 spec §5 S2 不可达（见 F-5.1） |
| 6 | 鉴权与请求校验 | `curl` 401/403/400/405/202 | **通过** |
| 7 | 非目标越界审计 | 代码/依赖/README 检索 | **通过**（无 TLS、无图片/文件、无中继） |
| + | 仓库交付完整性 | `git ls-files cmd` | **失败（阻塞）**：`cmd/clipshare/` 整个目录未被版本库跟踪（F1） |

**一句话结论**：实现行为与 spec 一致、可构建、可互通、不回环；但 `cmd/clipshare/` 被 `.gitignore`
误伤（F1，交付阻塞），另有 3 处低危偏差/待裁决项（F3/F4/F5）与 1 条操作注意（F2）。

## 1. 格式与静态检查

```console
$ cd /home/haitang/src/UserCode/tools
$ gofmt -l .
$ echo "gofmt exit=$?"
gofmt exit=0
$ go vet ./...
$ echo "vet exit=$?"
vet exit=0
$ CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
$ echo "win vet exit=$?"
win vet exit=0
```

- `gofmt -l .` 无输出（全仓库无未格式化文件），退出码 0。
- `go vet ./...` 无输出，退出码 0。
- 额外：`GOOS=windows` 的 vet 同样干净（跨平台编译期问题提前暴露）。

**通过。**

## 2. 全量测试（`-race -count=1`）

```console
$ go test ./... -race -count=1 ; echo "PIPESTATUS=$?"
ok  	clipshare/cmd/clipshare	1.014s
ok  	clipshare/internal/clip	1.466s
ok  	clipshare/internal/config	1.015s
ok  	clipshare/internal/discovery	1.315s
ok  	clipshare/internal/engine	12.970s
ok  	clipshare/internal/history	1.140s
ok  	clipshare/internal/model	1.009s
ok  	clipshare/internal/sync	1.325s
ok  	clipshare/internal/tray	1.049s
ok  	clipshare/internal/webui	1.015s
PIPESTATUS=0
```

10 个包全绿，无跳过、无竞态告警，退出码 0。`engine` 耗时 12.97s 属集成测（真实端口 + 两实例）成本。

**通过。**

## 3. Linux 交叉构建

```console
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...   # exit=0
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags '-s -w -X main.version=0.1.0' -o /tmp/cs-verify/clipshare-linux-2 ./cmd/clipshare
$ file /tmp/cs-verify/clipshare-linux-2
ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped
$ /tmp/cs-verify/clipshare-linux-2 --version
clipshare 0.1.0
```

- `go build ./...`（多包模式只做编译检查）退出码 0。
- 按 Makefile 同等参数（`-trimpath -ldflags "-s -w -X main.version=0.1.0"`）产出真实二进制：静态链接、已 strip、约 9.8 MB，`--version` 输出 `0.1.0`（ldflags 注入生效，与 `/v1/health` 上报同源）。

**通过。**

## 4. Windows 交叉构建

```console
$ CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...   # exit=0
$ CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
    -ldflags '-s -w -X main.version=0.1.0' -o /tmp/cs-verify/clipshare-windows-2.exe ./cmd/clipshare
$ file /tmp/cs-verify/clipshare-windows-2.exe
PE32+ executable (console) x86-64, for MS Windows
```

两个平台都在 `CGO_ENABLED=0` 下通过，符合 spec §2「免 cgo 交叉编译」。

**通过。**

## 5. 双进程实测（同机两实例）

### 5.1 配置

强制按任务要求：端口 `47901`/`47902`、各自独立 `--config-dir`、同一 `roomKey`、互为 seed、`--no-tray`。
为避免同机共享 X 剪贴板把「远端应用」误当成「本机复制」，两个实例的 `config.json` 里显式 `autoApply: false`
（这是合法配置，目的是让「是否转发/回环」可观测；`autoApply: true` 的 S2 行为另见 §5.4）。

```console
$ curl -s http://127.0.0.1:47901/v1/health
{"nodeId":"gggg…","name":"A","version":"dev","roomHash":"94a97baff738"}
$ curl -s http://127.0.0.1:47902/v1/health
{"nodeId":"hhhh…","name":"B","version":"dev","roomHash":"94a97baff738"}
```

`roomHash = sha256("verifyroom7")[:12] = 94a97baff738`，两端一致；`version` 为 `dev` 是因为本次手工
`go build` 未注入 ldflags（Makefile 产物为 `0.1.0`，§3 已验证）。

启动日志（`--config-dir /tmp/cs-verify/{g,h}`）：

```text
[engine] 启动完成：节点 B（hhhhhh），端口 47902，房间 94a97baff738
本地历史页: http://127.0.0.1:47903/ui
[engine] 启动完成：节点 A（gggggg），端口 47901，房间 94a97baff738
本地历史页: http://127.0.0.1:42715/ui     # 47902 已被 B 占用，回退到系统分配端口
```

> 注意：`47901`/`47902` 是相邻端口，而 UI 端口 = 同步端口 + 1（spec §9）。因此**必须先启动 B（47902）**，
> 否则 A 的 UI 会先占住 `127.0.0.1:47902`，导致 B 直接启动失败。详见 F2。

### 5.2 节点发现（seeds 路径，`--mdns=false`）

```text
19:23:00  A: [engine] 广播条目 cd28… 到 1 个节点（失败 0）        # A 启动即探活成功
19:23:20  B: [engine] 广播条目 bffe… 到 1 个节点（失败 0）        # B 在 30s 重扫后收编 A
```

符合 spec §7（seeds 立即探活 + 每 30s 重试）；`mdns:false` + seeds 的禁用多播路径可用。

### 5.3 推送、历史与不回环（决定性观测）

```console
# (a) 向 47901 的 /v1/items 推送 E1（正确 Bearer + X-Room）
$ curl -s -w 'HTTP %{http_code}\n' -X POST http://127.0.0.1:47901/v1/items \
    -H "Authorization: Bearer verifyroom7" -H "X-Room: 94a97baff738" \
    -H 'Content-Type: application/json' \
    -d '{"id":"3333333333333333","origin":"curlclient","originName":"CURL","content":"final-e1-1923","recvAt":"2026-11-11T00:00:00Z"}'
HTTP 202
A 历史: [{"id":"33279fb144f03ad4","origin":"curlclient","originName":"CURL",
          "content":"final-e1-1923","recvAt":"2026-11-11T19:23:02.779479985+08:00"}]
B 历史: []
```

- `id` 被服务端按 `origin+content` 重算（`3333…` → `33279fb1…`），`recvAt` 被重写为服务端本地时间（不信任发送方），符合 spec §4/§6。
- **B 历史为空 → A 收到入站条目不转发**（spec §5 S2；实现上 `applyRemote` 只写剪贴板/不广播）。

```console
# (b) 向 47902 推送一条**不同内容** E3（此时 B 已把 A 当 peer，见 5.2）
$ curl … http://127.0.0.1:47902/v1/items -d '{"content":"final-e3-1924",…}'   → HTTP 202
A 历史: []                                   # 未被 B 转发/回环
B 历史: [{"id":"0b7bbd1f1121e145", … "content":"final-e3-1924"}]

# (c) 再向 47901 推送另一条 E4
$ curl … http://127.0.0.1:47901/v1/items -d '{"content":"final-e4-1924",…}'   → HTTP 202
A 历史: [{"id":"ec62c519bf552356", … "content":"final-e4-1924"}]
B 历史: [{"id":"0b7bbd1f1121e145", … "content":"final-e3-1924"}]   # B 未被 A 转发
```

双向都**没有**把「收到的条目」再发给对端，因此不存在回环；A 的 `recvAt` 在后续重推中保持不变。

```console
# (d) 在 47902 上用**相同条目**重推两次（任务要求的回环检查）
#     时间窗口 19:23:03–04，早于 (b)(c)；此时 A 只有 E1、B 只有 E2
repost#1 202 ; repost#2 202
A 历史: 1 条（E1），recvAt 仍为 19:23:02.779…（未被刷新）
B 历史: 1 条（未新增），但 recvAt 由 19:23:03.632 → 19:23:04.892（见 F3）
```

结论：
1. **「重推不会回环到 47901」成立**：A 的历史条数与 `recvAt` 均未变化，且 B 没有向 A 发出任何入站条目（§5.3(b)(c) 用**不同内容**验证过——若 B 转发，A 一定会多出一条）。
2. **任务字面要求的「47902 的历史接口里出现推给 47901 的条目」不成立，且按 spec 不可达**：入站条目不会触发转发（spec §5 S2 + 回环不变量 §5 核心），因此只有「A 自己产生内容」（S1/S4）才会到达 B。详见 F-5.1。

### 5.4 默认 `autoApply=true` 时的 S2 应用行为（19:18 观察）

```text
A: [engine] 已应用远端条目 c75834919b688ff4（来自 CURL，18 字节）
$ DISPLAY=:1 xsel -ob
curl-live-A-191834
```

远端条目确实写入本机 X 剪贴板并 `MarkSeen`（不再被轮询当成本机复制 → 不广播）。同一观察窗口里 B 的
历史也出现了该内容，但 `origin` 是 **B 自己**（`"origin":"39feb5…","originName":"B"`）——那是**两个进程共享同一个
X 剪贴板**导致的本地重识别，不是 A 转发。这也解释了为什么用「默认配置 + 同机两进程」做验收时，
「B 看到 A 收到的内容」这个现象会出现，但它是同机模拟的假象，不能当作 P2P 转发证据。

## 6. 鉴权与请求校验（`curl`）

```console
$ # 错误 Bearer
$ curl -s -w 'HTTP %{http_code} ' -X POST …:47901/v1/items -H "Authorization: Bearer nope" -H "X-Room: 94a97baff738" -d '{"content":"x"}'
HTTP 401 {"error":"roomKey 不匹配"}
$ # 错误 X-Room（Bearer 正确）
HTTP 403 {"error":"X-Room 与本地 roomHash 不一致（多半是两台机器配了不同的 roomKey）"}
$ # 内容为空
HTTP 400 {"error":"内容为空"}
$ # 内容超过上限（262145 > 262144）
HTTP 400 {"error":"内容 262145 字节，超过上限 262144"}
$ # 刚好等于上限（262144）→ 接受
HTTP 202
$ # 非 POST
$ curl -o /dev/null -w '%{http_code}' :47901/v1/items
405
$ # /v1/health 免鉴权
$ curl -o /dev/null -w '%{http_code}' :47902/v1/health
200
```

401/403/400/405 与 spec §6 一致，错误体为 `{"error":"…"}`；边界值（= 上限）接受、超 1 字节拒绝。

**通过。**

## 7. 「非目标」越界审计

对照 spec §1 的非目标逐条检索（`grep` 覆盖 `internal/` 与 `cmd/` 非测试代码）：

| spec 非目标 | 检索 | 结论 |
|---|---|---|
| 图片 / 文件 / 富文本 | `image.`/`multipart`/`upload` 仅在 `internal/tray`（图标生成）与 `image/png` 出现；剪贴板接口只有 `Read/Write(string)` | 未越界（图标是运行时 PNG 生成，不是剪贴板图片支持） |
| 跨网段 / NAT / 中继 | `relay|nat|upnp|multipart` 无命中；只有 mDNS + seeds | 未越界 |
| 账号体系 / 多房间 | 无 `switch room`/`multiroom`；配置里只有单个 `roomKey` | 未越界 |
| TLS / 重放防护 | `crypto/tls|tls.Dial|tls.Listen|x509` 无命中；协议为明文 HTTP/1.1 | 未越界 |
| 历史加密落盘 | 无 `encrypt|aes|decrypt`；`history.json` 明文 | 未越界 |
| 安装包 / 开机自启 | 无 `autostart|systemd|startup` | 未越界 |
| 移动端 | 仅有 linux/windows/darwin 构建标签 | 未越界 |

依赖面同样干净：`atotto/clipboard`、`energye/systray`、`grandcat/zeroconf`（含 `miekg/dns`）、`godbus`、
`x/crypto`（systray/zeroconf 的间接依赖）——没有引入 TLS/中继/图片相关库。
README 也把上述条目明确写成了「非目标/已知行为」（README §6、§8）。

**通过。**

## 8. Findings

### F1（阻塞）`cmd/clipshare/` 整个目录未被版本库跟踪，交付物缺入口

`.gitignore` 第 3 行 `clipshare`（本意是忽略仓库根目录的构建产物二进制）在 gitignore 语义下会匹配
**任意层级**名为 `clipshare` 的文件或目录，于是 `cmd/clipshare/` 目录被整体忽略：

```console
$ git rev-parse --short HEAD
312e56d
$ git ls-files cmd | wc -l
0
$ git check-ignore -v cmd/clipshare/main.go
.gitignore:3:clipshare	cmd/clipshare/main.go
$ git ls-tree -r --name-only HEAD | grep '^cmd/' || echo "(HEAD 里没有 cmd/)"
(HEAD 里没有 cmd/)
$ git status --short          # 工作区“干净”，但 main.go 就在磁盘上
(无输出)
```

后果：`git clone` 出来的仓库**没有 `cmd/clipshare/main.go`**，无法构建二进制；`0d74bac "feat(M4): cmd/clipshare 组装 + README 终稿"` 实际只提交了 README（`git show --stat 0d74bac` 仅 1 个文件）。
因为 `git status` 一直显示干净，代码评审时极易漏掉。

最小复现（已实际执行）：

```console
$ git clone -q /home/haitang/src/UserCode/tools /tmp/repo-check && ls /tmp/repo-check/cmd
ls: 无法访问 '/tmp/repo-check/cmd': 没有那个文件或目录
$ cd /tmp/repo-check && go build ./cmd/clipshare
stat /tmp/repo-check/cmd/clipshare: directory not found
$ echo $?
1
$ ls /tmp/repo-check
docs  go.mod  go.sum  internal  Makefile  README.md
```

建议修复（单行，属交付/构建配置，不是实现逻辑）：

```diff
- clipshare
- clipshare.exe
+ /clipshare
+ /clipshare.exe
```

然后 `git add cmd/clipshare/main.go cmd/clipshare/main_test.go`，用 `git ls-files cmd` 确认非空。
**在此之前不应做最终提交/发版**（否则交付物缺入口）。

### F2（中，操作注意）同机两进程 + 相邻端口时，UI 端口（port+1）会与另一实例的同步端口撞车

spec §9 已明文规定「UI 端口 = 同步端口 + 1，占用时回退并打印实际地址」，所以这**不是实现违规**；
但任务指定的 `47901`/`47902` 恰好相邻，启动顺序会决定成败：

```console
# 先启动低端口实例 C（47901）→ UI 占住 127.0.0.1:47902
$ ss -ltn | grep 4790
LISTEN 127.0.0.1:47902
LISTEN *:47901
# 再启动 47902 实例 D
$ /tmp/cs-verify-bin --port 47902 --room verifyroom7 --name D --seed 127.0.0.1:47901 \
    --no-tray --mdns=false --config-dir /tmp/cs-verify/d
clipshare: 端口 47902 无法监听（可能已被占用），用 --port 或配置里的 port 换一个: listen tcp :47902: bind: address already in use
$ echo $?
1
```

反向顺序（先 47902、后 47901）则正常，低端口实例的 UI 回退到随机端口并在日志打印实际地址（本次 `127.0.0.1:42715`）。
建议：README/验收脚本里写明「同机模拟请先启动高端口实例，或使用非相邻端口（如 47901/47911）」；
若要彻底消除，可让 UI 端口可配置或由 engine 暴露 UI 挂载点（跨包改动，非必须）。

### F3（低，待裁决）重复入站条目会刷新既有条目的 `RecvAt` 并置顶，与 spec §5 S6「不写历史」字面冲突

`internal/history/history.go` 的 `Add` 在内容已存在时执行
`s.entries[idx].RecvAt = e.RecvAt` 并把该条搬到队首、返回 `false`（包注释称「与主流剪贴板管理器一致」）。
`sync.Server` 在 `Add` 前已把 `RecvAt` 重写为服务端本地时间，于是「重推同一内容」会刷新时间戳：

```text
重推前 B: recvAt = 2026-11-11T19:23:03.632772087+08:00
重推×2 后 B: recvAt = 2026-11-11T19:23:04.892137800+08:00，条数仍为 1
```

- 不影响回环（不 `apply`、不广播），也不产生新条目；
- 但 spec §5 S6 写的是「202 并丢弃，不写历史」，而 §5 的「有意的取舍」又假设重复不会刷新时间戳。
- 需裁决：改 spec（承认「重复只刷新本地时间戳」）或改 store（入站重复时保留原 `RecvAt`，历史包的去重刷新行为留给本机复制路径）。

最小复现：对同一实例连续两次 `POST /v1/items` 相同 `content`，观察 `/ui/api/entries` 的 `recvAt` 变化（条数不变）。

### F4（低）`/v1/health` 缺少 spec §6 示例中的 `clients` 字段

```console
$ curl -s http://127.0.0.1:47901/v1/health
{"nodeId":"…","name":"A","version":"dev","roomHash":"94a97baff738"}
```

spec §6 的响应示例含 `"clients":3`。发现逻辑只用 4 个字段，功能无影响；建议补上（= 当前 peers 数）
或改 spec 去掉该字段，避免文档与实现漂移。

### F5（低）`go.mod` / `go.sum` 未 tidy

```console
$ go mod tidy -diff
--- current/go.mod
+++ tidy/go.mod
 require (
-	github.com/atotto/clipboard v0.1.4 // indirect
+	github.com/atotto/clipboard v0.1.4
+	github.com/energye/systray v1.0.3
+	github.com/grandcat/zeroconf v1.0.0
+)
+require (
 	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
...
diff current/go.sum tidy/go.sum   # 缺 github.com/pkg/errors、golang.org/x/sync 等条目
```

三个直接依赖被标成 `// indirect`，`go.sum` 也缺条目（t2 已备注「留给收尾 tidy」）。构建不受影响，
但属于交付卫生问题，建议收尾前执行 `go mod tidy` 并提交。

### F6（已修复，记录）`engine.New` 曾忽略 `--config-dir`

t5 报告的「带 `--config-dir` 运行时托盘开关写回默认目录」在 `312e56d` 已修（`config.Config.Dir`
由 `config.Load` 填入、`engine` 复用）。本次以 `--config-dir` 运行期间 `~/.config/clipshare` 始终不存在，
未产生第二份配置；`internal/config` 与 `internal/engine` 新增的回归测试均在 §2 中通过。**无需再处理。**

### F-5.1（说明，非缺陷）任务第 5 项的字面期望与 spec 冲突

任务写的是「向 47901 推一条条目，确认 47902 的历史接口里出现该条目」。按 spec §5：
S2（收到远端条目）只入历史 + 可选写剪贴板，**不转发**；§5 的核心不变量也要求「程序写入的内容不得回播」。
因此「A 收到 → B 也看到」这条链路在协议里不存在；能做到「B 看到」的只有 S1/S4（A 自己产生内容）。
若真按字面实现（收到后转发），会直接违反回环不变量并造成风暴。

本次用两种方式给出了等价、且更强的验证：

1. 双向**不转发**（§5.3(b)(c)，用不同内容验证，B 已把 A 当 peer 时仍然不转发）；
2. S2 的应用语义（§5.4）：入站条目写本机剪贴板 + `MarkSeen`，不产生回广播。

建议后续验收脚本把这一条改成「A 复制（S1）→ B 历史出现 + B 剪贴板更新 → B 不反播」，
在真两机上执行（本机两进程共享同一个 X 剪贴板，无法模拟出「只有 A 复制」的干净场景）。

## 9. 未覆盖与限制

以下项本轮**未验证**（超出可自动化范围，需人工/真机）：

- Windows 真机运行（仅做了 `GOOS=windows` 构建 + vet；未跑 Windows 单测/实机）。
- 托盘菜单实际点击（`--no-tray` 运行；DBus/StatusNotifierItem 交互需在桌面会话人工点）。
- mDNS 启用路径（本次显式 `--mdns=false` 只走 seeds；mDNS 仅由单测覆盖）。
- 真两台机器同步（同机两进程共享 X 剪贴板，只能验证协议面；剪贴板隔离场景无法模拟）。
- Web UI 的浏览器交互（t3 已用 headless Chrome 实测；本轮只校验了 `/ui` 200、`/ui/api/entries`、`/ui/api/clear` 与缓存头）。

## 10. 复现环境与清理

- 所有实例均以 `--no-tray --mdns=false --config-dir /tmp/cs-verify/<dir>` 启动，验证后已 `SIGTERM` 全部退出；
- 验证结束 `pgrep -af cs-verify-bin` 为空，`47900–47909` 端口全部释放；
- 仓库内未产生任何构建产物或配置（二进制在 `/tmp/`）；`git status` 仅本报告为新增文件；
- 默认配置目录 `~/.config/clipshare` 全程未被创建（`--config-dir` 生效、flag 不写回配置）。
