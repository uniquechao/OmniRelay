# 项目 Agent Runtime

项目 PM 的运行环境独立于 OmniRelay Go 镜像。构建上下文为本目录。镜像内含 Python、固定版本的 DSH SDK 和原生 Harness、项目 PM 初始化模板、受限 profile 和 `context_whoami` 插件。Go 负责成员授权、工具数据与回复签名。

```sh
docker build -t omnirelay-dsh-runtime:group-request runtime/dsh
bash tests/integration/group-agent-gate.sh
```

## 内部验收

在仓库根目录运行上面的脚本。需要 Linux、Docker、Go（版本见 go.mod）、Python、OpenSSL 和 curl。脚本构建镜像，默认启动独立 PostgreSQL 16，也可用 `OMNIRELAY_TEST_DATABASE_URL` 指定隔离数据库。模型响应、账号、群和话题均为合成数据。Go、项目成员授权、双向 TLS、镜像内 DSH、工具和卷都是真实运行。两项 Go 测试都必须实际执行通过；在普通测试命令中因依赖缺失而跳过的结果不能计入验收。本地日志在脚本建立的 `.runtime/group-gate.*`。

| 门槛 | 放行条件 |
| --- | --- |
| Gateway 与权限验证器 | 同一来源重投复用工作，第二个 Go 领取者拿不到活跃工作。无会籍、撤权后重加、群绑定版本变化、账号停用均无法沿用旧授权。任何拒绝都不会产生一条回复 |
| Runtime 与工具权限 | 私有接口验证服务证书。错误调用方、owner、归属代次、领取代次、实例、会话、轮次及伪造用户参数均被拒。本轮模型只看到 `context_whoami`；Go 只返回本轮账号和项目的四个字段 |
| 恢复与回程 | 两名成员共用项目 PM，各自得到正确的身份结果。重启镜像内的 Runtime 和 DSH 后，原卷可找回已完成的工作。回到原群原话题，署名来自 Go 的项目名称。发送前撤权会抑制交付，已开始发送但无回执则记录为未知 |

验收经内部 Channel 调用与产品相同的 `group.Accept`、Gateway、权限、编排、工具回调和投递流程。没有飞书接入时可独立通过以上门槛。飞书适配器单独负责核验群提及、将身份和群绑定转换成内部请求、以原消息 ID 投递回复。

## 运行配置

| 组件 | 配置 |
| --- | --- |
| Go 私有服务 | `OMNIRELAY_AGENT_RUNTIME_URL` 指向 owner 的稳定 HTTPS 地址。主机名对应 Runtime 的证书 SAN。配置 `OMNIRELAY_AGENT_CA`、`OMNIRELAY_AGENT_CERT`、`OMNIRELAY_AGENT_KEY`，以及私有监听地址 `OMNIRELAY_AGENT_LISTEN`（默认 :8444）。未配置 Runtime 地址时保留原服务行为 |
| Runtime 身份 | `AGENT_OWNER` 对应上述主机名，`AGENT_GATEWAY_URL` 指向私有 Go 服务，`AGENT_GATEWAY_NAME` 匹配 Go 客户端证书的 DNS SAN。`AGENT_CA`、`AGENT_CERT`、`AGENT_KEY` 为只读挂载的文件。服务默认监听 8443 |
| 模型与存储 | 提供 `DEEPSEEK_BASE_URL`、`DEEPSEEK_API_KEY`、`DSH_MODEL`。挂载可供 UID/GID 10001 写入的卷到 `/var/lib/omnirelay-agent`。只读根文件系统时给 `/tmp` 挂载可执行的临时文件系统，原生 Harness 会在其中装入动态库 |
| 飞书入口 | 启用原服务的 Bot 长连接与发送配置后，才将新收到的群身份查询接入。一个可用例句是「@Bot 我是谁」。已有历史消息和其他意图继续沿用原处理策略 |

每个 Runtime 卷有一个常驻写者，每个 Agent 还有实例锁。模板在首次创建时复制。Go 共享 PostgreSQL 持久化业务工作、owner、权限绑定、工具回执、交付 ID 和外部回执；Runtime 用本地 SQLite 保存接受与完成记录，DSH 单独保存会话。若进程在只读轮次完成前中断，恢复后允许重新执行该读取。飞书发送发起后未知的结果需要按原交付 ID 对账。

本期完成单 owner 原卷恢复。双节点自动迁移需另外实现旧写者隔离和卷交接。现有镜像只开放这个无参只读插件。模型、工具和桥处在同一容器信任边界；新增任意文件、命令或第三方插件前需为非可信执行进程提供独立身份和挂载。
