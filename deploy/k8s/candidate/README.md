# 候选验收入口

在目标机的唯一受控工程目录使用 `python3 deploy/k8s/candidate/manage.py`。下文命令名均为此入口的参数。管理身份独占 `deploy/k8s/private/candidate/`，保存绑定、实际清单、凭据、运行状态和证据。设置 `PYTHONDONTWRITEBYTECODE=1`。候选使用独立命名空间、无令牌服务账号、独立 TLS PostgreSQL、迁移和运行角色以及专用 PVC。目标集群须支持 NetworkPolicy、动态 PVC 和镜像拉取。未登记网页验收入口时，候选 Go 服务仅暴露健康与数据库就绪探针。飞书应用凭据继续由产品网页管理。

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

## OAuth 与目录首验

目标机管理员先用 `web` 登记私有 JSON，再启动候选。字段 `origin` 为独立 HTTPS 主机，`productionOrigin` 为产品入口；`appID`、`tenantKey` 取自网页已配置的应用；`bootstrap` 是候选库的首位管理员。另填 `managerNode`、`ingressClass`、`ingressNamespace`、`ingressLabels`、`tlsSecret`。使用支持关闭访问日志的 ingress-nginx，证书 Secret 位于候选命名空间。注册 DNS 与证书后，由应用管理员在同一飞书应用中添加精确地址 `<origin>/auth/feishu/callback`。在实际入口复核 TLS、主机名及代理日志配置。

候选 Pod 固定到管理机节点，只读挂载管理目录的 `web/` 子目录。目标机需要支持 POSIX ACL 的文件系统及 `setfacl`；管理入口只对候选 UID 10000 授予目录遍历、连接套接字、读取本次运行文件的权限。执行 `start` 后启动或重启 `service`。候选探针独立运行，网页在 `open` 发布运行凭据后开放；撤销或过期后入口关闭。候选数据库单独开通测试账号并分配系统角色或本地项目会籍。

凭据适配服务用私有 Unix 套接字逐连接调用同版本二进制的 `omnirelay candidate-operation`。例如 systemd socket 使用 `Accept=yes`，服务使用 `StandardInput=socket` 和 `StandardOutput=socket`，监听套接字限管理身份连接。服务运行环境复用网页设置的解密密钥、产品 HTTPS 来源和对网页配置表的只读数据库连接；另设 `OMNIRELAY_CANDIDATE_ORIGIN` 及 `OMNIRELAY_CANDIDATE_ATTEMPTS`。后者是适配服务独占的 0700 持久目录，用于排除同一次尝试的重复派发。应用密钥和平台令牌仅在该受控进程内参与换码或采集。

在 `register` 的 `resources` 内登记固定回调 `callback`、验收者 `identity` 及需要的 `department`、`person`、`group`。这里的 `group` 指通讯录授权范围返回的用户组。部门样本选用平台授权根或全组织授权下的指定测试部门。将 `oauth.identity` 映射到 `OAuth`，将 `directory.scope`、`directory.sample` 映射到 `目录`，两个场景的 `socket` 填适配服务套接字。先执行 `preflight`，再用 `open` 绑定所选代号、操作、有效期和次数。有预先清点的人数时传入 `expectedPeople`。一次成功登录需一次换码，完整目录同步需一次范围读取和一次样本读取。

验收者使用专用账号分别批准和取消。以候选日志确认回跳结果，以工作台落点、当前账号及 `/api/v1/me` 核对身份。按运行代号关联管理侧 `events.jsonl`，其中的 `component` 为实际适配进程可执行文件的 SHA-256。取消在候选回调结束。目录记录包含预期人数、实际去重人数、部门数、成员页数、成员续页数，以及缺少激活状态和头像的人数；只有实际人数与预期一致且成员续页数大于零时，才记录真实成员多页通过。真人身份、平台授权和字段缺项按场景记录，测试账号用匿名代号。浏览器取证关闭 HAR、追踪及录屏。

## 专用群与私聊消息验收

完成隔离故障验证后，从接收端自己的完整 Git SHA 构建 Go 镜像，将该值传给构建参数 `OMNIRELAY_SOURCE_SHA`，按既有 Go 发布流程使用镜像摘要上线。发布时保留生产 Bot 的 Lease 和 TLS。应用清单从发布时记录的上一镜像摘要读取回退点。首次发布保持 `Secret/omnirelay-candidate-receiver` 缺省，复核普通收信后，再按验收窗口下发该 Secret 的 `token` 键并使配置生效。令牌是独立的 32 字节随机值的十六进制编码。仅管理服务可经私有 HTTPS 路径访问 `/internal/candidate-messages`。

通过 `receiver` 的标准输入登记私有对象，字段为接收端 HTTPS 来源 `origin`、接收端源码 `sha`、相同的 `token` 和上一镜像摘要 `rollback`。消息场景复用既有受控套接字，无需登记额外适配器套接字。在 `register` 添加匿名的 `chat` 专用群及 `privateChat` 私聊绑定，可选登记一个允许携带的 `file` 来源。确保测试者在生产和候选均使用同一个飞书应用身份，并拥有登记项目的读取权限。回复需项目评论权限。每次 `open` 选择要启用的会话和 `message.receive`，需要发送或对账时分别选择 `message.reply`、`message.receipt`。需要在 Web 取证时，同一 run 登记 `oauth.identity` 和固定回调。次数涵盖换码、发送、重试及对账；队列领取和确认不占发送次数。

测试者登录候选后，以 `GET /api/v1/candidate/messages` 和 `/api/v1/me` 核对账号及逻辑工作。使用正常工作台会话及 CSRF 头调用 `POST /api/v1/candidate/messages/{messageID}/reply`，请求只含 `text`。回执未知时请求同一来源的 `POST /api/v1/candidate/messages/{messageID}/receipt` 并核对平台。只有平台关联能够确认原操作时才更新未知状态。服务为候选从已确认的入站记录确定目标。管理侧 `events.jsonl` 记录匿名 run、候选 SHA、接收端 SHA 和接收端回退摘要。接收端用 `event_ref` 关联归属已提交日志与 SDK 回调返回日志，`callback_millis` 从 SDK 业务回调接收事件计到该回调返回。线协议复验记录平台侧看到的 ACK 状态和往返耗时。

`hold-messages` 关闭转交，`revoke-outbound` 单独撤销出站，`revoke` 关闭两者。回退前先撤销运行，再按既有 Go 回退流程恢复上一镜像；复核普通收信，已固定的候选工作不进入生产待路由状态。候选停机或进程重启后可在同一 Pod 和有效 run 内续领；重新锁定新 Pod 会撤销旧 run。代理的消息用于验证候选业务处理，候选 SDK 握手和代次另按长连接流程演练。
