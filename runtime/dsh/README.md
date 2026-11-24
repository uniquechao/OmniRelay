# 项目 Agent Runtime

项目 PM 和全局查询助手使用独立于 OmniRelay Go 服务的 Runtime 镜像。本目录是其构建上下文，包含固定版本的 DSH SDK、原生 Harness、两类人设初始化模板、身份工具和项目 PM 业务工具桥。Harness 管理定时器。Go 内部的 Gateway 和权限模块决定使用哪个 Agent、它可以读取哪些字段，以及 Channel 最终向谁投递。

```sh
docker build -t omnirelay-dsh-runtime:group-request runtime/dsh
bash tests/integration/group-agent-gate.sh
```

## 系统管理员原生调试

系统管理员登录工作台，点击一级菜单 **dshbot**（`/#/dshbot`）即可打开官方 DSH Web。Go 对 `/dshbot/` 下的页面、资源、API 和 WebSocket 使用当前登录会话校验 `dsh.debug`，退出、停用或撤权后终止长连接。所有具有该权限的管理员共用一个调试工作区。调试会话与飞书、项目 PM 的会话分别保存。

在独立的调试服务账号下安装本目录的固定版本，将虚拟环境的 `bin` 加入该账号的 `PATH`，并从仓库根目录构建启动器。下面的地址须替换为工作台实际的 `OMNIRELAY_PUBLIC_ORIGIN`。

```sh
python -m pip install -r runtime/dsh/requirements.txt
go build -o bin/dshbot ./cmd/dshbot
bin/dshbot -origin https://relay.example.test \
  -home /var/lib/omnirelay-dshbot -socket /run/omnirelay-dshbot/web.sock
```

部署时为调试进程提供独立的系统身份、可写的 home 和专用调试模型配置。该进程启动 `dsh web`，仅监听回环地址（默认端口 3080，`-port` 可修改），在进程内交换原生登录凭据。为服务预建 socket 父目录，属主为调试账号，属组包含 Go 服务账号，目录权限设为 `2750` 以继承属组。启动器创建权限为 `0660` 的 socket；工作区仅授予调试账号访问。使用进程管理器同时管理启动器和其子进程，并在每次启动前准备专属运行目录。

Go 默认连接上述 socket，使用其他位置时设置 `OMNIRELAY_DSHBOT_SOCKET`。Go 和调试进程须能访问同一 socket。HTTPS 入口将整个 `/dshbot/` 路径原样转发给 Go，支持 WebSocket Upgrade 和流式响应。管理员遇到 HTTP 503 时，检查调试进程、socket 路径和属组权限。未登录请求返回 401，已登录且缺少权限的请求返回 403。

在 Kubernetes 中，使用仓库根目录为构建上下文，以 `deploy/k8s/base/dshbot/image.Dockerfile` 构建调试镜像。在 Go 所在命名空间，将 `deploy/k8s/base/dshbot/debug.yaml` 中的 `OMNIRELAY_DSHBOT_IMAGE` 替换为已验证的镜像后应用。独立 Deployment 的就绪探针通过 socket 验证原生页面返回 200，再将同目录的 `gateway-patch.yaml` 作为 strategic merge patch 应用到 Go Deployment。此卷仅向 Go 挂载 `ipc` 子目录。调试 Pod 的网络策略默认禁止入站和出站流量，调试工作区可离线使用；调用在线模型需要单独配置调试供应商、凭据和对应的出站访问。

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
| `TestProjectToolAudienceImageGate` | 内部群请求经真实 DSH 镜像、双向 TLS 在正确项目建任务，原话题得到署名回执，个人工作台显示结果 |

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

Go 侧使用 `bootstrap-gateway-tls.sh` 签发独立身份，在 Go 容器只读挂载 `omnirelay-agent-gateway-tls`，配置下表的 CA、证书、私钥和 Runtime Pod 稳定地址。集群私有回调使用 [Gateway Service](../../deploy/k8s/base/dsh/gateway-service.yaml) 的 8444 端口。只启用站内对话时设置 `OMNIRELAY_AGENT_FEISHU_ENABLED=0`。镜像验收脚本另外执行 `TestWorkbenchChatGate`，覆盖签名回程、重放、连续对话和成员隔离。

独立部署使用 [StatefulSet 与内部 Service](../../deploy/k8s/base/dsh/runtime.yaml)。在目标工程构建带提交号的镜像，将清单中的 `omnirelay/dsh-runtime:release` 替换为该不可变版本。先用 `bash deploy/k8s/scripts/bootstrap-dsh-tls.sh CONTEXT NAMESPACE PRIVATE_DIRECTORY` 签发单实例服务证书，签发目录须由部署者保管。将清单在指定命名空间执行服务端 dry-run 后应用。Pod 将在专属卷中建立工作日志，私有端口 8443 使用双向 TLS。Go 后续接入时须使用同一 CA 签发、带 Gateway DNS 身份的独立客户端证书。

运行 `bash deploy/k8s/scripts/configure-dsh-model.sh CONTEXT NAMESPACE` 可在终端隐藏输入 Key。脚本写入 `omnirelay-dsh-model` Secret，等待新内容投影到 Pod，再发一次真实 Harness 合成请求，成功输出 `model ok`。其他供应商可在清单中配置兼容 Messages 的 `DEEPSEEK_BASE_URL` 与 `DSH_MODEL`。挂载文件优先于环境变量，每个新轮次读取当前 Key。

Pod 的 `livez` 探针检查私有 TLS 服务。`readyz` 检查本地工作日志可读和模型凭据已提供；真实模型认证由显式 `python probe.py model` 检查。等待首次配置 Key 时 Pod 可持续运行并保持未就绪。模型冒烟使用临时会话、固定提示和无业务工具的 profile。接通 Go 私有回调后，业务轮次才可调用项目工具。

| 组件 | 配置 |
| --- | --- |
| Go 私有服务 | `OMNIRELAY_AGENT_RUNTIME_URL` 指向 owner 的稳定 HTTPS 地址。主机名对应 Runtime 的证书 SAN。配置 `OMNIRELAY_AGENT_CA`、`OMNIRELAY_AGENT_CERT`、`OMNIRELAY_AGENT_KEY`，以及私有监听地址 `OMNIRELAY_AGENT_LISTEN`（默认 :8444）。未配置 Runtime 地址时保留原服务行为 |
| Runtime 身份 | `AGENT_OWNER` 对应上述主机名，`AGENT_GATEWAY_URL` 指向私有 Go 服务，`AGENT_GATEWAY_NAME` 匹配 Go 客户端证书的 DNS SAN。`AGENT_CA`、`AGENT_CERT`、`AGENT_KEY` 为只读挂载的文件。服务默认监听 8443 |
| 模型与存储 | 提供 `DEEPSEEK_BASE_URL`、`DEEPSEEK_API_KEY`、`DSH_MODEL`。挂载可供 UID/GID 10001 写入的卷到 `/var/lib/omnirelay-agent`。只读根文件系统时给 `/tmp` 挂载可执行的临时文件系统，原生 Harness 会在其中装入动态库 |
| 飞书入口 | 启用 Bot 长连接与发送配置后，新收到的群「@Bot 我是谁」和私聊「我是谁」可进入各自的 Agent。启用发送适配器和 Runtime 后可登记定向提醒 |

每个 Runtime 卷有一个常驻写者，每个 Agent 还有实例锁。模板在首次创建时复制。Go 共享 PostgreSQL 持久化业务工作、owner、权限绑定、工具回执、交付 ID 和外部回执；Runtime 用本地 SQLite 保存接受与完成记录，DSH 单独保存会话。若进程在只读轮次完成前中断，恢复后允许重新执行该读取。飞书发送发起后未知的结果需要按原交付 ID 对账。

本期覆盖单 owner 原卷恢复、私聊身份查询和单次项目提醒。每个 Runtime 同时托管到期时间最早的四个计划，Go 接受原定时器到期后十分钟内的回调。跨节点自动迁移仍需旧写者隔离和卷交接。业务会话仅开放无参身份工具，定时会话只可激活已授权的定时器。模型、工具和桥处在同一容器信任边界。
