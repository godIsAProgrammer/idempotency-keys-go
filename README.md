# idempotency-keys-go

极简「HTTP 写操作幂等 key 缓冲池」服务,基于 Go 标准库 `net/http` / `encoding/json` /
`regexp` / `sync` / `time` 实现,不依赖任何第三方包。

客户端在执行 HTTP 写操作前,先用 `Idempotency-Key` 在本服务登记;服务器按
`(scope, key)` 唯一记录请求指纹与响应,避免同一个 key 被重试时把业务侧动作做多次。

## 端点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 返回 `{"ok":true}` |
| POST | `/records` | 开始或复用一条幂等记录,返回 `outcome` |
| GET | `/records` | 列出记录,可用 `?status=` / `?scope=` 过滤 |
| GET | `/records/{key}?scope=` | 取记录详情 |
| DELETE | `/records/{key}?scope=` | 删除一条记录(主要用于测试 / 后台清理) |
| POST | `/records/{key}/complete?scope=` | 把 `in_flight` 标记为 `completed`,落响应 |
| POST | `/records/{key}/fail?scope=` | 把 `in_flight` 标记为 `failed`,落响应 |
| GET | `/metrics` | 总数 / 按状态 / 按 scope / expired 计数 |

## 状态机

```
                   POST /records (key, scope, fp)
                              |
        +---------------------+---------------------+
        |                                           |
   first time                            existing & not expired
   create record                                    |
   status = in_flight                fp matches?    +-- yes -> outcome ∈ {in_flight,completed,failed}
   outcome = started                                |          attempt_count += 1
                                                    +-- no  -> 409 fingerprint_mismatch

            POST /records/{key}/complete           POST /records/{key}/fail
                       |                                   |
                       v                                   v
              status = completed                 status = failed
              completed_at = now                 completed_at = now
```

`in_flight` 之后的 `complete` / `fail` 都会要求记录还没过期,过期 → 410 `key_expired`。
重复 `complete` / `fail` → 409 `already_finished`。

## 数据模型

`POST /records` 请求体:

```json
{
  "key": "idem_payments_001",
  "scope": "payments.create",
  "request_fingerprint": "1111111111111111111111111111111111111111111111111111111111111111",
  "ttl_seconds": 3600
}
```

| 字段 | 约束 |
| --- | --- |
| `key` | 必填,匹配 `^[A-Za-z0-9_-]{8,128}$` |
| `scope` | 必填,匹配 `^[a-z][a-z0-9._-]{0,63}$`,例如 `payments.create` |
| `request_fingerprint` | 必填,64 位小写 hex(由客户端对请求体做 sha256) |
| `ttl_seconds` | 选填,默认 86400,最大 604800 |

返回示例:

```json
{
  "outcome": "started",
  "record": {
    "key": "idem_payments_001",
    "scope": "payments.create",
    "request_fingerprint": "11111111...",
    "status": "in_flight",
    "attempt_count": 1,
    "created_at": "2026-05-10T05:00:00Z",
    "expires_at": "2026-05-11T05:00:00Z"
  }
}
```

`outcome` 取值:

| outcome | HTTP | 含义 |
| --- | ---: | --- |
| `started` | 201 | 新建一条 in_flight 记录(也可能是覆盖了一条已过期的) |
| `in_flight` | 200 | 已存在,后端正在处理,客户端应等待 |
| `completed` | 200 | 已完成,可以直接复用 `response_status` + `response_body` |
| `failed` | 200 | 上次失败,客户端可决定是否重试 |

`POST /records/{key}/complete?scope=...` 请求体:

```json
{
  "scope": "payments.create",
  "response_status": 201,
  "response_body": {"order_id": "ord_demo_001"}
}
```

`scope` 在 query 和 body 都可以传,二者必须一致(否则 `scope_mismatch`)。
`response_status` 必须是 100..599 的整数;`response_body` 选填,需为合法 JSON。

## 默认数据

启动时内存里有 4 条 fixture:

| key | scope | status | 备注 |
| --- | --- | --- | --- |
| `idem_completed_demo` | `payments.create` | `completed` | 响应 201 + `{"order_id":"ord_demo_001",...}` |
| `idem_inflight_demo` | `orders.refund` | `in_flight` | 模拟正在跑的请求 |
| `idem_failed_demo` | `payments.create` | `failed` | 响应 422 + `{"error":"insufficient_funds"}` |
| `idem_expired_demo` | `payments.create` | `in_flight` | 已过期,`expires_at < now` |

Default fixture 的 `request_fingerprint` 用 `aaaa...` / `bbbb...` 这种 64 位 hex 占位,
便于本地手测用同 fingerprint 重发。

## 本地运行

本机需要 Go 1.23 或更高版本。项目只用标准库,不需要 `go get`。

```bash
go test ./...
go run .
```

默认监听 `0.0.0.0:8803`,可通过 `PORT` 覆盖:

```bash
PORT=18803 go run .
```

## 请求示例

健康检查:

```bash
curl http://127.0.0.1:8803/health
```

开始一条幂等记录:

```bash
curl -X POST http://127.0.0.1:8803/records \
  -H 'content-type: application/json' \
  -d '{"key":"idem_payments_demo_001","scope":"payments.create",
       "request_fingerprint":"1111111111111111111111111111111111111111111111111111111111111111"}'
```

完成:

```bash
curl -X POST 'http://127.0.0.1:8803/records/idem_payments_demo_001/complete?scope=payments.create' \
  -H 'content-type: application/json' \
  -d '{"response_status":201,"response_body":{"order_id":"ord_demo_001"}}'
```

完成后再 begin:

```bash
curl -X POST http://127.0.0.1:8803/records \
  -H 'content-type: application/json' \
  -d '{"key":"idem_payments_demo_001","scope":"payments.create",
       "request_fingerprint":"1111111111111111111111111111111111111111111111111111111111111111"}'
# outcome=completed,客户端直接用 record.response_status / record.response_body
```

按 status 列表:

```bash
curl 'http://127.0.0.1:8803/records?status=in_flight'
```

监控:

```bash
curl http://127.0.0.1:8803/metrics
```

## 关键文件

- `idem/store.go`:`Store` 内存存储,`Record` / `BeginInput` / `CompleteInput`,`BeginRecord` / `CompleteRecord` / `FailRecord` / `EvictExpired`,`ValidationError` / `ConflictError` / `GoneError`,`DefaultStore` 提供 fixture。
- `idem/server.go`:`NewServer(store)` 与 `Handler()`,`handleRecords` / `handleRecordItem` / `handleMetrics`,`decodeJSON` / `sendJSON` / `sendMethod`。
- `main.go`:入口,从 `PORT` 环境变量读端口(默认 `8803`)。
- `idem/store_test.go`:领域逻辑测试,覆盖 fingerprint 冲突、过期覆盖、状态机、metrics、evict。
- `idem/server_test.go`:HTTP 端到端测试,覆盖各 outcome、错误码、未知路由、method_not_allowed。

## Docker 环境

确保 Docker Desktop 已启动。

```bash
docker build -t idempotency-keys-go .
docker run --rm -p 8803:8803 idempotency-keys-go
```

服务启动后:

```bash
curl http://127.0.0.1:8803/health
# 预期 {"ok":true}
```

容器内验证:

```bash
docker run --rm idempotency-keys-go go test ./...
docker run --rm idempotency-keys-go pwd       # /app
docker run --rm idempotency-keys-go git status --short  # 干净
```

## 常见问题

### 为什么 `(scope, key)` 一起做主键,而不是只用 key?

不同业务用同一类客户端生成的 idempotency key 有可能撞,绑定 scope(如 `payments.create` /
`orders.refund`)能避免一个动作的 key 被另一个动作的请求复用。客户端只要确保单个
scope 内 key 唯一,跨 scope 不需要全局唯一。

### `request_fingerprint` 由谁算?为什么强制 sha256 hex?

由调用方算请求体(经过 canonical JSON 化 + 参数排序后)的 sha256,然后传 64 位小写 hex。
强制 hex 让服务侧只做格式校验,不解析二进制;长度固定也方便 `crypto/subtle.ConstantTimeCompare`
之类的等长比较(如有需要)。

### 过期记录 `BeginRecord` 为什么不是 410,而是直接覆盖?

如果已过期,说明这条 key 已经不再被业务跟踪,客户端再来 begin 等价于一次全新的请求,
直接覆盖创建一条新 in_flight 比要求客户端先 DELETE 再 POST 友好;`Complete` / `Fail` 操作
过期记录则 410,因为这两个动作意味着客户端以为还在原来的事务里。
