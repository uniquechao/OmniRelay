# 候选验收入口

在目标机的唯一受控工程目录使用 `python3 deploy/k8s/candidate/manage.py`。下文命令名均为此入口的参数。管理身份独占 `deploy/k8s/private/candidate/`，保存绑定、实际清单、凭据、运行状态和证据。设置 `PYTHONDONTWRITEBYTECODE=1`。候选使用独立命名空间、无令牌服务账号、独立 TLS PostgreSQL、迁移和运行角色以及专用 PVC。目标集群须支持 NetworkPolicy、动态 PVC 和镜像拉取。候选 Go 服务仅暴露健康与数据库就绪探针。飞书应用凭据继续由产品网页管理。

1. 在工作分支将候选提交同步至仓库，再在目标工程同步该提交。运行 `init`，通过标准输入传入私有 JSON 对象。字段为 `context`、镜像仓库路径 `repository`、完整分支引用 `ref`、`productionNamespace`、生产 Lease 名 `productionLease`，以及 `productionDatabase`、`productionObjects`。后两项分别包含生产 Service 名 `service` 和后端端口 `port`。管理身份需能读取生产端点、核验服务账号权限以及管理独立命名空间。真实参数均从本地登记。
2. 运行 `start <完整源码 SHA>`。入口核对远端分支及干净的目标工程，从该提交归档用既有 Dockerfile 构建、推送并按摘要启动，调用原有 Go 迁移。启动完成后核验实际 Pod 镜像、网络边界、生产端点和 Lease 权限。`lock` 在 Pod 重建后重新核对并撤销旧 run。`stop` 撤销授权并停止验收服务，保留候选库与存储。目标工程同步至上一候选 SHA 后执行 `rollback`，按记录的上一摘要恢复。启停与修订记录见私有目录的 `events.jsonl`。
3. 运行 `register`，在标准输入提供私有 JSON。顶层 `owner` 指定维护人，`resources` 按 `identity/session/project/chat/file` 分组，每组是「L1 匿名代号 → 真实标识」的映射。一个账号及一个群即可开始登记。按需增加会话、项目和其他代号。`capabilities` 以 OAuth、目录、消息、文件为键，每项含负责人 `owner` 和受控适配服务的本机 Unix 套接字绝对路径 `socket`。`operations` 将允许的精确操作名映射到场景。`evidence` 可登记现有 SQL 所需的 `app_id/tenant_key/message_id/account_id`。
4. `preflight` 分项生成只读就绪报告，可传入按资源类型选择匿名代号的 JSON 参数。`samples` 读取现有 L1 匿名样本。`demo` 在本地执行样本读取并演示无绑定时的缺项、负责人和恢复条件。`evidence` 对已锁定的候选库执行既有只读 SQL。原始结果保存在私有目录。
5. `open` 从标准输入读取 `issue`、`resources`（各类型选择一个代号）、允许的 `operations` 数组、正整数 `count` 和有效秒数 `ttl`。真实写运行至少绑定身份、会话和项目；具体操作另需对应资源。返回的 `run/credential/expires` 只交给本次测试调用方。命令输出重定向至权限 0600 的私有文件。`revoke` 在目标机使当前授权失效。

## 受控调用协议

管理身份启动 `service`，监听私有目录中的 `service.sock`。对独立调用身份只授予通往套接字的目录遍历权限和套接字连接权限。管理状态、适配服务套接字和产品凭据保持管理身份专用。同一目标使用同一管理目录及文件锁。

每次连接收发一行 UTF-8 JSON。调用方发送 `version=1`、`run`、`credential`、`operation` 及业务 `payload`。服务端从登记信息取资源，核对实际候选、授权期限、操作和剩余次数，落盘扣额后持锁同步派发。撤销、换版和新授权与派发共用互斥锁。结果未确认时冻结该 run 并保留已扣次数。

后续适配服务接收 `method=readiness` 时只读检查指定场景与绑定，返回布尔值 `ready`。接收 `method=execute` 时按服务端传来的 `resources` 限定对象，仅接受该操作的业务字段，在实际写入前复核 `expires`，按 `attempt` 去重并同步完成，禁止排队延后写入。返回同一 `attempt` 和 `finished=true` 表示已终结。适配服务通过产品受控服务接口使用既有应用身份，调用方只持本次 run 凭据。

单节点共用 Docker 运行时时，目标登记可指定 `delivery=node` 和 `buildNode`（节点的主机名标签）。此时镜像只保存在构建节点，迁移与服务均固定到该节点，锁定时核对 Pod 实际运行的 Docker 内容摘要。目标机可通过 `GO_MODULE_PROXY`、`CANDIDATE_PIP_INDEX` 复用网络镜像源。
