# 环境说明

- 项目语言:Go,运行在 Go 1.23
- Docker 基础镜像:`golang:1.23`
- 容器工作目录:`/app`
- 构建时会把项目根目录的仓库文件复制到 `/app`
- 项目只使用 Go 标准库(`net/http` / `encoding/json` / `regexp` / `sync` / `time` / `testing`),不需要第三方依赖
- 默认启动命令:`go run .`,监听 `0.0.0.0:8803`
- 默认验证命令:`go test ./...`
- HTTP 端点:`GET /health`、`POST /records`、`GET /records`、`GET /records/{key}`、`DELETE /records/{key}`、`POST /records/{key}/complete`、`POST /records/{key}/fail`、`GET /metrics`
- 默认 fixture 共 4 条:`idem_completed_demo` / `idem_inflight_demo` / `idem_failed_demo` / `idem_expired_demo`
- 状态机:`in_flight` → (`completed` 或 `failed`);过期记录直接 410 `key_expired`
- TTL:默认 86400 秒,上限 604800 秒(7 天)
- Dockerfile 会把 `/app` 初始化为 `main` 分支 Git 仓库,并创建一个初始提交

## 手动验证命令

```bash
docker build -t idempotency-keys-go .
docker run --rm -d -p 8803:8803 --name idem-qc idempotency-keys-go
curl http://127.0.0.1:8803/health
curl http://127.0.0.1:8803/records
curl 'http://127.0.0.1:8803/records/idem_completed_demo?scope=payments.create'
# 第一次开始一个新 key
curl -X POST http://127.0.0.1:8803/records \
  -H 'content-type: application/json' \
  -d '{"key":"idem_demo_new_001","scope":"payments.create",
       "request_fingerprint":"1111111111111111111111111111111111111111111111111111111111111111"}'
# 同 key 同 fingerprint 重发,outcome=in_flight,attempt_count=2
curl -X POST http://127.0.0.1:8803/records \
  -H 'content-type: application/json' \
  -d '{"key":"idem_demo_new_001","scope":"payments.create",
       "request_fingerprint":"1111111111111111111111111111111111111111111111111111111111111111"}'
# 标记完成
curl -X POST 'http://127.0.0.1:8803/records/idem_demo_new_001/complete?scope=payments.create' \
  -H 'content-type: application/json' \
  -d '{"response_status":201,"response_body":{"order_id":"ord_demo"}}'
# 完成后再 begin,outcome=completed,直接复用响应
curl -X POST http://127.0.0.1:8803/records \
  -H 'content-type: application/json' \
  -d '{"key":"idem_demo_new_001","scope":"payments.create",
       "request_fingerprint":"1111111111111111111111111111111111111111111111111111111111111111"}'
# 同 key 不同 fingerprint -> 409 fingerprint_mismatch
curl -X POST http://127.0.0.1:8803/records \
  -H 'content-type: application/json' \
  -d '{"key":"idem_demo_new_001","scope":"payments.create",
       "request_fingerprint":"2222222222222222222222222222222222222222222222222222222222222222"}'
# 过期记录 -> complete 返 410
curl -X POST 'http://127.0.0.1:8803/records/idem_expired_demo/complete?scope=payments.create' \
  -H 'content-type: application/json' \
  -d '{"response_status":200}'
curl 'http://127.0.0.1:8803/metrics'
docker stop idem-qc
docker run --rm idempotency-keys-go go test ./...
docker run --rm idempotency-keys-go pwd
docker run --rm idempotency-keys-go git status --short
```
