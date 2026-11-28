# 项目 Agent Runtime

全局管理、项目 PM 和知识问答共用独立 Runtime 镜像。Go Gateway 提供角色、项目和逐轮授权并控制结果交付。Runtime 管理原生 Harness 实例、会话、定时器和业务工具桥。

```sh
docker build -t omnirelay-dsh-runtime:group-request runtime/dsh
bash tests/integration/group-agent-gate.sh
```

### 角色、版本及工作恢复

Gateway 向 `/works/{work}` 提交包含 `role`、`config_version`、可选 `persona` 的工作。角色为 `management-pm`、`project-pm` 或 `query`。版本 0 使用默认模板。每个会话轮次启动时载入该角色的模板和本版配置，首次成功加载在实例目录记录版本与人设摘要。原生会话、工作区和逻辑 Agent ID 在配置更新后保持稳定。

正式聊天启动前，Runtime 将本工作绑定 POST 到 Gateway 的 `/internal/tools/project/catalog`。Gateway 按当前授权返回目录，模型只能调用其中的业务工具及身份工具。知识问答收到带写能力的目录时拒绝执行。固定身份查询及定时通知使用专用提示。应用请求按文本输入处理。

工作结果带原生会话、轮次、实际使用的配置版本和结果种类。`write_slots` 列出成功写入的序号。`failed_writes` 列出 Gateway 明确拒绝的序号、工具和状态码。正式业务对象与逐项结果以 Go 的事务回执为准。重试先查询同一工作，已完成的结果从 SQLite 读取。运行中失去写入确认时，在封存结果或恢复执行前用原序号向 Gateway 对账。

旧卷就地升级并保留原生会话、工作与槽位。旧版本没有逐槽成功位，其历史操作需以 Go 事务回执核对。原卷首次初始化产生的 `persona.md` 与 `origin.json` 保留用于迁移核验。在 Runtime 容器运行 `python instance.py legacy-personas` 可列出与首建模板有差异或文件缺失的实例。按 Agent ID 核对租户和项目后，管理员可在对应 Agent 配置中保存所需自定义内容。工具只输出文件位置和摘要。

固定版 SDK 的本地回归可在装好 `requirements.txt` 后运行 `python runtime/dsh/test_runtime.py`。测试使用原生 Harness、本机合成模型和受控的工具目录。

## 系统管理员原生调试

系统管理员登录工作台，从 **dshbot** 打开官方 DSH Web。Gateway 对页面、资源、WebSocket 和 RPC 校验浏览器会话与 `dsh.debug`。通过后使用 DSH 原生 owner 权限，退出或撤权会断开活动连接。

正式控制台运行在 `omnirelay-dsh-prod` 的 `console` 容器内。预览项目 Bot 使用 `preview-project-bot` 和 `preview-dsh-runtime`。两个预览工作负载的服务地址、存储和检查命令见[预览 Bot 与 DSH Runtime](../../deploy/k8s/README.md#预览-bot-与-dsh-runtime)。

Web 容器随生产 Runtime StatefulSet 启动。两个入口使用同一个模型 Secret。管理页按工作区列出 Agent 原有的原生会话。打开历史调用 DSH 的 JSONL 读取接口，继续会话和改名使用同一文件及原生写锁。管理员在项目工作区新建的会话也写入该 Agent 的 store。在管理页主动占用会话后，可在该会话执行 `/handoff`，将空闲会话交还给项目 Bot。

构建 `deploy/k8s/base/dshbot/image.Dockerfile`，将镜像填入 [Runtime 清单](../../deploy/k8s/base/dsh/runtime.yaml) 的 `OMNIRELAY_DSHBOT_IMAGE`。应用 [IPC 卷清单](../../deploy/k8s/base/dshbot/debug.yaml)，并对 Gateway 应用同目录的 `gateway-patch.yaml`。Gateway 只读挂载 IPC 目录。Web 容器挂载 Agent 数据、管理工作区及模型 Secret，Runtime 的工作账本和 mTLS 客户端私钥由工作容器持有。

## 内部验收

在仓库根目录运行 `bash tests/integration/group-agent-gate.sh`。需要 Linux、Docker、Go（版本见 go.mod）、Python、OpenSSL 和 curl。脚本默认启动独立 PostgreSQL 16，也可用 `OMNIRELAY_TEST_DATABASE_URL` 指定隔离数据库。模型响应、账号和收件地址使用合成数据。真实 Go 服务逻辑、会籍、双向 TLS、镜像内 Harness 定时器、工具和持久卷均参与验收。脚本要求下列验收测试逐一通过，跳过任一测试即失败。日志位于脚本输出的 `.runtime/group-gate.*` 目录。

| 门槛 | 放行条件 |
| --- | --- |
| `TestGatewayPermissionGate` | 群请求重投复用原工作，第二个 Go 领取者拿不到活跃工作。撤权重加、群绑定版本变化和账号停用均使旧授权失效 |
| `TestRuntimeImageGate` | 私有接口验证证书、owner、代次、实例、会话和轮次。两个成员各自收到项目 PM 在原话题的署名回复。容器重启后从原卷恢复。发送前撤权会抑制投递，发出后回执不明会记录为未知 |
| `TestPrivateReplyGate` | 启用账号私聊「我是谁」，即使没有项目会籍也能收到全局查询助手的本人回复。冒用另一私聊会话被拒。同一成员在群和私聊使用独立 Agent 与会话，重复事件只回复一次 |
| `TestDirectedNoticeGate` | 通过应用接口登记一次群和个人提醒。Runtime 自行发现计划，Harness 登记定时器。到期前重启 Runtime 后，同一原生定时器触发两个独立工作，内部 Channel 各有一条回执。群会话和群消息都不含个人标记。重复登记和触发均可重试 |
| `TestNoticePermissionGate` | 普通成员、非项目收件人、错误 owner 代次、错误定时器标识及错误触发时间均被拒。撤销个人会籍只抑制该人的投递，其余有效目标继续处理。停用的计划拒绝后续回调 |
| `TestWorkbenchChatGate` | 同一项目按账号隔离对话；重复消息复用工作，回答带当前项目署名 |
| `TestProjectPMToolMatrix` | 23 个目录项逐项验证业务结果和权限，缺少任一目录项的测试即失败 |
| `TestProjectToolRecoveryGate` | 并发和跨代次重试仅写入一次；相同命令序号换参数、过期轮次和撤权重放被拒 |
| `TestProjectToolHistoryGate` | 来源撤权后抑制交付与历史正文；后续模型会话与受限正文隔离 |
| `TestLiveGroupBoardReply` / `TestLiveGroupReplySemantics` | 真实模型识别当前未完成任务及明确提醒意图。隔离群通道核对已关闭事项、原话题、负责人和重复事件。 |
| `TestProjectToolAudienceImageGate` | 内部群请求经真实 DSH 镜像、双向 TLS 在正确项目建任务，原话题得到署名回执，个人工作台显示结果 |
| `TestAgentRolesImageGate` | 在正式工作台查询获准的纪要正文和修订、区分仅标题结果、汇总允许的项目、按原用户委派给项目 PM 并查询子工作。原生 Runtime 回调后按正式任务记录与投递回执对账。 |

镜像还会验证 SQLite 命令日志重新打开后保留待核对的写入及其序号。目录、角色与每个工具的放行条件见[项目 PM 工具验收表](../../design/engineering/project-pm-tool-capabilities.md)。

内部 Channel 复用产品的入站、路由、权限、工具回调和投递实现。另有适配器契约测试检查飞书私聊入站、原消息回复，以及用 `chat_id` 或 `open_id` 新发消息的请求和回执。这些门槛均可在没有飞书账号和凭据的环境中运行。

## 定向提醒

拥有当前项目 `project.settings` 权限的项目成员通过工作台认证和 CSRF 校验后，可调用 `POST /api/v1/projects/{projectID}/agent-notices`。登记单次提醒时须提供 `Idempotency-Key` 和精确到秒的未来时间。例如，以下请求体同时指定一个已绑定项目群和一个有效项目成员：

```json
{
  "at": "2030-03-05T08:00:00Z",
  "targets": [
    {"mode": "group_notice", "target": "<项目绑定的群 ID>"},
    {"mode": "person_notice", "target": "<项目成员的内部账号 ID>"}
  ]
}
```

接口返回 HTTP 202 和计划 ID。同一操作者以同一键提交相同计划会取回原 ID，改用不同内容返回 409。用 `DELETE /api/v1/projects/{projectID}/agent-notices/{planID}` 停用计划。每个目标分别保存一份授权，工具取数和最终投递都会复核。人员变动后，旧计划不会自动授权给新成员。

Runtime 通过双向 TLS 发现归属自己的计划，为每个计划保留独立的定时会话。Harness 唤醒后只回报原生定时器标识、归属代次和发生时间。Go 从已登记的计划中取得收件人，分别驱动群与个人的 DSH 会话，签上 Agent 名和空间域后请求 Channel 发送新消息。私聊查询直接回复原私聊消息，两种主动提醒均无需历史入站消息。

## 运行配置

工作台的「项目 → Bot」以当前登录会话向 `/api/v1/projects/{projectID}/bot/messages` 提交消息。Go 核验 CSRF、账号和当前会籍，按成员与项目保留独立 DSH 会话。回复从模型轮次持久化到成员自己的对话记录，署名从 Go 的项目档案生成。页面可展示最近 50 条问答，撤权后再次打开会重新检查权限。

Go 侧使用 `bootstrap-gateway-tls.sh` 签发独立身份，在 Go 容器只读挂载 `omnirelay-agent-gateway-tls`，配置下表的 CA、证书、私钥和证书覆盖的 Runtime 稳定地址。集群私有回调使用 [Gateway Service](../../deploy/k8s/base/dsh/gateway-service.yaml) 的 8444 端口。只启用站内对话时设置 `OMNIRELAY_AGENT_FEISHU_ENABLED=0`。镜像验收脚本另外执行 `TestWorkbenchChatGate`，覆盖签名回程、重放、连续对话和成员隔离。

独立部署使用 [StatefulSet 与内部 Service](../../deploy/k8s/base/dsh/runtime.yaml)。在目标工程构建带提交号的镜像，将清单中的 `omnirelay/dsh-runtime:release` 替换为该不可变版本。先用 `bash deploy/k8s/scripts/bootstrap-dsh-tls.sh CONTEXT NAMESPACE PRIVATE_DIRECTORY` 签发单实例服务证书，签发目录须由部署者保管。将清单在指定命名空间执行服务端 dry-run 后应用。Pod 将在专属卷中建立工作日志，私有端口 8443 使用双向 TLS。Go 后续接入时须使用同一 CA 签发、带 Gateway DNS 身份的独立客户端证书。

运行 `bash deploy/k8s/scripts/configure-dsh-model.sh CONTEXT NAMESPACE` 可在终端隐藏输入 Key。脚本写入 `omnirelay-dsh-model` Secret，等待新内容投影到 Pod，再发一次真实 Harness 合成请求，成功输出 `model ok`。其他供应商可在清单中配置兼容 Messages 的 `DEEPSEEK_BASE_URL` 与 `DSH_MODEL`。挂载文件优先于环境变量，每个新轮次读取当前 Key。

Pod 的 `livez` 探针检查私有 TLS 服务。`readyz` 检查本地工作日志可读和模型凭据已提供；真实模型认证由显式 `python probe.py model` 检查。等待首次配置 Key 时 Pod 可持续运行并保持未就绪。模型冒烟使用临时会话、固定提示和无业务工具的 profile。接通 Go 私有回调后，业务轮次才可调用项目工具。

| 组件 | 配置 |
| --- | --- |
| Go 私有服务 | `OMNIRELAY_AGENT_RUNTIME_URL` 指向 owner 的稳定 HTTPS 地址。主机名对应 Runtime 的证书 SAN。配置 `OMNIRELAY_AGENT_CA`、`OMNIRELAY_AGENT_CERT`、`OMNIRELAY_AGENT_KEY`，以及私有监听地址 `OMNIRELAY_AGENT_LISTEN`（默认 :8444）。未配置 Runtime 地址时保留原服务行为 |
| Runtime 身份 | `AGENT_OWNER` 对应上述主机名，`AGENT_GATEWAY_URL` 指向私有 Go 服务，`AGENT_GATEWAY_NAME` 匹配 Go 客户端证书的 DNS SAN。`AGENT_CA`、`AGENT_CERT`、`AGENT_KEY` 为只读挂载的文件。服务默认监听 8443 |
| 模型与存储 | 提供 `DEEPSEEK_BASE_URL`、`DEEPSEEK_API_KEY`、`DSH_MODEL`。挂载可供 UID/GID 10001 写入的卷到 `/var/lib/omnirelay-agent`。只读根文件系统时给 `/tmp` 挂载可执行的临时文件系统，原生 Harness 会在其中装入动态库 |
| 飞书入口 | 启用 Bot 长连接与发送配置后，新收到的群「@Bot 我是谁」和私聊「我是谁」可进入各自的 Agent。启用发送适配器和 Runtime 后可登记定向提醒 |

每个 Runtime 卷有一个常驻写者，每个 Agent 还有实例锁。Go 使用 PostgreSQL 持久化业务工作、owner、权限绑定、工具回执、交付 ID 和外部回执。Runtime 在本地 SQLite 保存工作结果，DSH 保存原生会话。若进程在只读轮次完成前中断，恢复后可重新执行该读取。飞书发送结果未知时按原交付 ID 对账。

同一 owner 按发现接口返回的全部有效计划恢复或启动原生单次定时器。计划在 Go 侧失效后，发现循环停止对应的时钟进程。Go 接受到期后十分钟内的回调并逐目标复核。跨节点迁移需要旧写者隔离和卷交接。
