# 泄洪闸门操作协调服务

## 项目目标

该服务用于水工控制室协调泄洪闸门的命令申请、安全联锁复核和单次开度调整。系统在同一闸门上只允许一条已通过复核且尚未结束的执行命令，任何冲突都必须被显式拒绝，不能覆盖已经占用的闸门。现场人员可把闸门切到本地手动控制，再切回中控；切换与命令执行互斥，未执行的中控授权在接管时被显式撤销，切回中控必须依据现场复核的真实开度重新确认。

## 使用角色

- 运行协调员：登记闸门并提交开度调整命令。
- 安全复核员：提交联锁结论，决定命令是否通过。
- 闸门操作员：凭执行令牌启动命令并登记执行结果。
- 现场人员/调度员：把闸门切到本地手动控制，确认现场真实开度后切回中控。

## 核心实体

- `gate`：闸门及其当前开度、运行状态、控制模式、最大开度和活动命令。
- `gate_command`：一次开度调整申请，包含目标开度、申请原因、状态和执行令牌。
- `interlock_decision`：安全复核结论，状态为 clear 或 blocked。

## 业务流程

### register-gate

运行协调员通过 `POST /gates` 登记闸门。服务校验标识、名称、最大开度和初始开度，写入空闲状态；重复标识返回稳定冲突错误，不覆盖原记录。

### request-gate-command

运行协调员通过 `POST /commands` 提交开度调整命令。服务读取闸门当前开度和限制，校验目标开度与申请信息，创建待复核命令并冻结起始开度。

### review-safety-interlock

安全复核员通过 `POST /commands/{id}/interlock` 提交复核结论。clear 结论会原子占用闸门并签发一次执行令牌；blocked 结论会把命令置为 rejected。其他命令先占用同一闸门时，当前 clear 结论返回冲突且不改变闸门。

### execute-gate-command

闸门操作员通过 `POST /commands/{id}/execute` 提交执行令牌。服务校验命令、令牌、闸门占用关系和目标开度，在临界区内完成 approved、executing、completed 或 aborted 的转换，并同步更新闸门状态。令牌在命令结束后失效，不能复用。

### switch-gate-local

现场人员通过 `POST /gates/{id}/local` 接管闸门。移动中或故障中的闸门拒绝接管；闸门空闲或被占用时切换都在同一临界区内完成：闸门置为 local + idle 并清空活动命令引用，该闸门上所有 pending、approved 的中控授权原子撤销为 revoked 并失效其令牌。切换与执行、复核并发时由同一把锁决定唯一赢家。

### revoke-gate-command

调度员通过 `POST /commands/{id}/revoke` 显式撤销一条尚未执行的中控授权。只有 pending、approved 命令可以撤销；approved 命令撤销时同步释放闸门占用。已完成、已中止、已拒绝的历史不可撤销、不可改写。

### switch-gate-central

现场处理完毕后，调度员通过 `POST /gates/{id}/central` 并携带现场实测的 `confirmed_aperture` 切回中控。服务按当时真实条件校验开度范围，把闸门真实开度写入记录；之后新建的命令以该开度冻结起始值，旧的已撤销授权不会复活。切回前若现场发现闸门故障，必须先在现场完成故障清除。

## 状态与规则

- `gate` 状态：idle、reserved、moving、faulted。
- `gate` 控制模式：central（默认）、local。
- `gate_command` 状态：pending、approved、rejected、executing、completed、aborted、revoked。
- 只有 pending 命令可以复核。
- 只有 approved 命令可以执行，且执行令牌必须匹配。
- clear 复核与闸门占用必须同时成功；任一步失败时两者都不写入。
- blocked 复核使命令 rejected，不会占用闸门。
- 模拟故障会令命令 aborted、闸门 faulted，并保留最终开度。
- 已完成或已中止的命令不能再次执行。
- local 模式下不能新建中控命令、clear 复核不能占用闸门、执行不能启动移动。
- 本地接管原子撤销该闸门全部 pending、approved 授权；revoked 是单向终态，联锁记录等历史保持不变。
- 移动中的闸门不能被接管；接管与执行并发时两者只有一个成功，且本地模式下绝不出现移动。
- 切回中控必须提供并校验现场真实开度；起始开度快照已与真实开度不符的旧命令在复核或执行时返回 `gate_conditions_changed` 冲突，需要重新申请。
- 模式切换不改变任何已完成、已中止、已拒绝命令的状态、令牌和开度记录。

## 模块边界

- `cmd/server`：进程入口、监听地址和退出控制。
- `internal/httpapi`：HTTP 路由、JSON 解码、状态码映射。
- `internal/coordination`：工作流编排、命令标识和执行令牌签发。
- `internal/domain`：实体、不变量和状态转换规则。
- `internal/storage`：带互斥保护的内存仓库和原子写入。
- `internal/contracts`：API 请求、响应和稳定错误结构。
- `internal/bootstrap`：运行时依赖装配。

## 接口

- `GET /health`
- `POST /gates`
- `GET /gates/{id}`
- `POST /gates/{id}/local`
- `POST /gates/{id}/central`
- `POST /commands`
- `GET /commands/{id}`
- `POST /commands/{id}/interlock`
- `POST /commands/{id}/execute`
- `POST /commands/{id}/revoke`

错误响应统一为：

```json
{"code":"stable_error_code","message":"可读说明"}
```

## 验证计划

本初始化基线刻意不生成单元测试、夹具或浏览器测试文件，也不声明 `test_command`。后续任务阶段负责补充测试，并在该阶段建立红绿验证。当前每条工作流通过 `cmd/workflowcheck` 启动真实 HTTP 服务并调用公开接口，校验成功状态、持久化结果和关键失败分支。`switch-gate-control` 额外通过并发请求验证切换与执行的互斥结果。
