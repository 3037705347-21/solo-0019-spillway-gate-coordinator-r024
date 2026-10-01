# 泄洪闸门操作协调服务

这是一个无外部依赖的 Go 后端，用本地内存仓库协调闸门登记、开度调整命令、安全联锁复核和执行结果。服务强调单闸门活动命令互斥、原子状态转换、执行令牌生命周期和稳定错误传播。

## 运行

```sh
go build ./...
go run ./cmd/server --addr 127.0.0.1:18119
```

健康入口为 `GET http://127.0.0.1:18119/health`。

## API

```text
POST /gates
GET  /gates/{id}
POST /gates/{id}/local
POST /gates/{id}/central
POST /commands
GET  /commands/{id}
POST /commands/{id}/interlock
POST /commands/{id}/execute
POST /commands/{id}/revoke
```

登记闸门示例：

```json
{
  "id": "gate-east",
  "name": "东侧泄洪闸",
  "max_aperture": 4.5,
  "initial_aperture": 0.5
}
```

提交命令示例：

```json
{
  "gate_id": "gate-east",
  "target_aperture": 2.25,
  "reason": "上游水位调整",
  "requested_by": "dispatcher-lin"
}
```

联锁复核示例：

```json
{
  "verdict": "clear",
  "verifier": "safety-wu",
  "note": "上下游条件满足"
}
```

执行示例：

```json
{
  "operator": "operator-chen",
  "token": "复核响应中的 execution_token",
  "simulate_fault": false
}
```

## 本地控制与切回中控

闸门默认处于 `central` 模式，`GET /gates/{id}` 的 `mode` 字段返回当前模式。

现场人员可接管闸门（移动中或故障中的闸门会被拒绝）。接管会原子撤销该闸门上所有尚未执行的中控授权（pending、approved 变为 `revoked`，令牌失效），闸门变为 `local` + `idle`，本地模式下中控命令不能占用闸门或启动移动：

```sh
curl -XPOST http://127.0.0.1:18119/gates/gate-east/local \
  -d '{"requested_by":"field-chen"}'
```

切回中控必须提供现场实测开度，服务据此重新确认真实条件；之后新命令以该开度为起始值，旧的已撤销授权不会复活：

```sh
curl -XPOST http://127.0.0.1:18119/gates/gate-east/central \
  -d '{"requested_by":"dispatcher-lin","confirmed_aperture":2.0}'
```

调度员也可以在中控模式下显式撤销一条尚未执行的授权（approved 时同步释放占用）：

```sh
curl -XPOST http://127.0.0.1:18119/commands/cmd-xxxx/revoke \
  -d '{"revoked_by":"dispatcher-lin","reason":"调度撤销"}'
```

模式切换不会改写任何 rejected、completed、aborted 的历史命令。

## 目录

- `cmd/server`：服务进程。
- `cmd/workflowcheck`：四条工作流的真实 HTTP 检查入口。
- `internal/bootstrap`：依赖装配。
- `internal/contracts`：公开数据契约。
- `internal/coordination`：业务工作流。
- `internal/domain`：实体、不变量和状态机。
- `internal/httpapi`：路由、解码和错误映射。
- `internal/storage`：互斥保护的内存仓库。

## 工作流检查

```sh
go run ./cmd/workflowcheck --workflow register-gate
go run ./cmd/workflowcheck --workflow request-gate-command
go run ./cmd/workflowcheck --workflow review-safety-interlock
go run ./cmd/workflowcheck --workflow execute-gate-command
go run ./cmd/workflowcheck --workflow switch-gate-control
```

这些检查会创建临时 HTTP 服务并调用真实路由。它们不调用外部网络，也不依赖数据库。

## 测试状态

测试有意延后。当前交付仅包含初始化基线和生产级 smoke 检查；后续任务阶段会添加自动化测试与红绿验证，不应把当前没有测试文件视为项目未完成。
