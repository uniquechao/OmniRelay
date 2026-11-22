# 项目 Agent Runtime

项目 PM 和全局查询助手使用独立于 OmniRelay Go 服务的 Runtime 镜像。本目录是其构建上下文，包含固定版本的 DSH SDK、原生 Harness、两类人设初始化模板和只读身份工具。Harness 管理定时器。Go 内部的 Gateway 和权限模块决定使用哪个 Agent、它可以读取哪些字段，以及 Channel 最终向谁投递。

```sh
docker build -t omnirelay-dsh-runtime:group-request runtime/dsh
bash tests/integration/group-agent-gate.sh
```

## 内部验收

在仓库根目录运行上面的脚本。需要 Linux、Docker、Go（版本见 go.mod）、Python、OpenSSL 和 curl。脚本默认启动独立 PostgreSQL 16，也可用 `OMNIRELAY_TEST_DATABASE_URL` 指定隔离数据库。模型响应、账号和收件地址使用合成数据。真实 Go 服务逻辑、会籍、双向 TLS、镜像内 Harness 定时器、工具和持久卷均参与验收。脚本要求下列五项测试逐一通过，跳过任一测试即失败。日志位于脚本输出的 `.runtime/group-gate.*` 目录。

| 门槛 | 放行条件 |
| --- | --- |
| `TestGatewayPermissionGate` | 群请求重投复用原工作，第二个 Go 领取者拿不到活跃工作。撤权重加、群绑定版本变化和账号停用均使旧授权失效 |
| `TestRuntimeImageGate` | 私有接口验证证书、owner、代次、实例、会话和轮次。两个成员各自收到项目 PM 在原话题的署名回复。容器重启后从原卷恢复。发送前撤权会抑制投递，发出后回执不明会记录为未知 |
| `TestPrivateReplyGate` | 启用账号私聊「我是谁」，即使没有项目会籍也能收到全局查询助手的本人回复。冒用另一私聊会话被拒。同一成员在群和私聊使用独立 Agent 与会话，重复事件只回复一次 |
| `TestDirectedNoticeGate` | 通过应用接口登记一次群和个人提醒。Runtime 自行发现计划，Harness 登记定时器。到期前重启 Runtime 后，同一原生定时器触发两个独立工作，内部 Channel 各有一条回执。群会话和群消息都不含个人标记。重复登记和触发均可重试 |
| `TestNoticePermissionGate` | 普通成员、非项目收件人、错误 owner 代次、错误定时器标识及错误触发时间均被拒。撤销个人会籍只抑制该人的投递，其余有效目标继续处理。停用的计划拒绝后续回调 |

内部 Channel 复用产品的入站、路由、权限、工具回调和投递实现。另有适配器契约测试检查飞书私聊入站、原消息回复，以及用 `chat_id` 或 `open_id` 新发消息的请求和回执。五项门槛均可在没有飞书账号和凭据的环境中运行。

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

| 组件 | 配置 |
| --- | --- |
| Go 私有服务 | `OMNIRELAY_AGENT_RUNTIME_URL` 指向 owner 的稳定 HTTPS 地址。主机名对应 Runtime 的证书 SAN。配置 `OMNIRELAY_AGENT_CA`、`OMNIRELAY_AGENT_CERT`、`OMNIRELAY_AGENT_KEY`，以及私有监听地址 `OMNIRELAY_AGENT_LISTEN`（默认 :8444）。未配置 Runtime 地址时保留原服务行为 |
| Runtime 身份 | `AGENT_OWNER` 对应上述主机名，`AGENT_GATEWAY_URL` 指向私有 Go 服务，`AGENT_GATEWAY_NAME` 匹配 Go 客户端证书的 DNS SAN。`AGENT_CA`、`AGENT_CERT`、`AGENT_KEY` 为只读挂载的文件。服务默认监听 8443 |
| 模型与存储 | 提供 `DEEPSEEK_BASE_URL`、`DEEPSEEK_API_KEY`、`DSH_MODEL`。挂载可供 UID/GID 10001 写入的卷到 `/var/lib/omnirelay-agent`。只读根文件系统时给 `/tmp` 挂载可执行的临时文件系统，原生 Harness 会在其中装入动态库 |
| 飞书入口 | 启用 Bot 长连接与发送配置后，新收到的群「@Bot 我是谁」和私聊「我是谁」可进入各自的 Agent。启用发送适配器和 Runtime 后可登记定向提醒 |

每个 Runtime 卷有一个常驻写者，每个 Agent 还有实例锁。模板在首次创建时复制。Go 共享 PostgreSQL 持久化业务工作、owner、权限绑定、工具回执、交付 ID 和外部回执；Runtime 用本地 SQLite 保存接受与完成记录，DSH 单独保存会话。若进程在只读轮次完成前中断，恢复后允许重新执行该读取。飞书发送发起后未知的结果需要按原交付 ID 对账。

本期覆盖单 owner 原卷恢复、私聊身份查询和单次项目提醒。每个 Runtime 同时托管到期时间最早的四个计划，Go 接受原定时器到期后十分钟内的回调。跨节点自动迁移仍需旧写者隔离和卷交接。业务会话仅开放无参身份工具，定时会话只可激活已授权的定时器。模型、工具和桥处在同一容器信任边界。
