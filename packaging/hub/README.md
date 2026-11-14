# 部署中继中心点（A）

B、C 各自在不同网络（NAT 后）时，需要一个双方都能访问的中心点做转发。
中继**只监听回环地址**，对外通过内网穿透/反向代理暴露 —— 请不要把它直接绑在公网接口上。

## 一键安装（推荐）

```bash
# 在 A 上（需要 root）
sudo ./install-hub.sh ./clipshare          # 二进制就在同目录时
# 可选：指定房间码与监听地址
sudo ROOM_KEY=2mnsn82k6b62 HUB_LISTEN=127.0.0.1:3011 ./install-hub.sh ./clipshare
```

脚本会：建 `clipshare` 系统用户 → 装二进制到 `/usr/local/bin` →
建 `/etc/clipshare`（首次启动自动生成自签证书）→ 装并启用 `clipshare-hub.service` →
打印**证书指纹**。

## 手工安装

```bash
sudo install -m755 clipshare /usr/local/bin/clipshare
sudo install -d -m700 /etc/clipshare
sudo cp config.example.json /etc/clipshare/config.json   # 填入 roomKey 等
sudo cp clipshare-hub.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now clipshare-hub
clipshare hub-info --config-dir /etc/clipshare           # 看指纹与在线节点
```

## Docker

```bash
cp config.example.json config.json    # 同目录，填好 roomKey
cp /path/to/clipshare ./              # linux/amd64 静态二进制
docker compose up -d
docker compose logs | grep 指纹
```

数据卷 `./data` 保存配置与自签证书 —— **证书丢了指纹就会变**，所有客户端都要重配 `relayPin`。

## 对外暴露

| 方式 | 要求 |
|---|---|
| 内网穿透（frp 等） | 用 **TCP 透传**类型，不要用 HTTP 反代：中继自己做 TLS，代理终止 TLS 会让指纹钉失效 |
| nginx 反代 | 必须 `proxy_read_timeout` > 5s 且关闭响应缓冲（`/v1/feed` 是长轮询） |

## 客户端（B/C）配置

```json
{
  "relayURL": "https://<公网地址>:<端口>",
  "relayPin": "sha256:<install-hub.sh 打印的指纹>",
  "relayEnabled": true,
  "relayIntervalMs": 800
}
```

`clipshare status` 会显示「中心点：…（已连通）」；托盘菜单里也能一键开关中继。

## 安全提醒

中继是明文转发，**A 能读到所有经过它的剪贴板内容** —— 请只部署在自有机器上。
中继强制 TLS（自签 + 指纹钉）；`hubAllowPlaintext: true` 仅用于内网调试。
