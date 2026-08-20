# task100-leasetoken

资源租约与 fencing token 服务。为命名资源颁发带 TTL 的租约，租约持有者获得单调递增的 fencing token，用于检测过期并安全释放。支持获取、续约、释放、心跳、转让、强制撤销、TTL 修改、资源锁定、过期清扫和重启恢复。所有状态（租约、fencing token 计数、资源、审计日志）持久化到 SQLite。

## 主要输入与输出

- 输入：HTTP JSON 请求（`/acquire`、`/renew`、`/release`、`/transfer` 等），含 `resource`、`holder`、`ttl_seconds`、`fencing_token` 等字段。
- 输出：JSON 响应（`lease_id`、`fencing_token`、`expires_at`、租约列表、审计记录、统计指标等）。管理员端点需 `X-Admin-Token` 头。

## 本地命令

```bash
go build ./...       # 编译
go run . --smoke-test  # 自检（不依赖外部服务、不依赖真实时间睡眠）
go run .             # 启动 HTTP 服务（默认 :8080，SQLite 文件 lease.db）
go test ./...        # 测试
```

## Docker 构建

构建脚本 `build_benzhi_docker.sh` 接收两个参数：

1. 镜像名（默认 `my-project`）
2. 目标平台（默认 `linux/amd64`）

```bash
# amd64
bash ./build_benzhi_docker.sh go-task-benzhi:amd64 linux/amd64
docker run -it go-task-benzhi:amd64
# arm64
bash ./build_benzhi_docker.sh go-task-benzhi:arm64 linux/arm64
docker run -it go-task-benzhi:arm64
```

进入容器后可用 `go version` 确认工具链版本为 `go1.26.3`。

## 双架构主镜像

主 `Dockerfile` 为多阶段构建（`golang:1.26.3-bookworm` 构建 + `alpine:3.20` 运行，`CGO_ENABLED=0`）：

```bash
docker buildx build --platform linux/amd64 --load -t go-task-check:amd64 .
docker run --rm go-task-check:amd64 --smoke-test
docker buildx build --platform linux/arm64 --load -t go-task-check:arm64 .
docker run --rm go-task-check:arm64 --smoke-test
```

## 技术栈

- Go `1.26.3`（`GOTOOLCHAIN=local`）
- SQLite 引擎 `3.46.1`，纯 Go 驱动 `modernc.org/sqlite v1.35.0`（`CGO_ENABLED=0`）
- 依赖下载：`GOPROXY=https://goproxy.cn,direct`、`GOSUMDB=sum.golang.google.cn`
