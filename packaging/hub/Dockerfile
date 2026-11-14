# 中继中心点的最小镜像：静态二进制 + 自签证书（首次启动生成在挂载卷里）。
# 构建：docker build -t clipshare-hub .
#   （需要先把对应平台的 clipshare 二进制放在本目录，或用 build-arg 指定）
FROM scratch
COPY clipshare /clipshare
COPY config.json /etc/clipshare/config.json
EXPOSE 3011
ENTRYPOINT ["/clipshare"]
CMD ["--config-dir", "/etc/clipshare"]
