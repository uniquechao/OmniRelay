# OmniRelay

<img src="design/prototype/omni-relay-logo-bicolor.png" alt="OmniRelay Logo" width="280">

**群里问进度，群里交办任务，关键变化每天汇总。**

OmniRelay 是面向 **500 人以内单飞书组织**的项目助手，采用自托管架构。成员在群里 @ Bot 或发起私聊，就能查询项目任务、交办事项、查看当前风险。

**工作台已实现账号登录、用户管理、系统级角色与飞书通讯录。** 项目协作和 Bot 卡片可继续使用离线原型体验。

![OmniRelay 工作台示例，展示项目进展、关键任务和阻塞事项](design/prototype/omni-relay-workbench-preview.png)

## 产品特性

- **一句话查进度。** 在飞书问「今天还有哪些任务卡住了？」，查看任务状态、负责人、截止时间和来源。群内回复遵循该群的公开范围。
- **交办后有回执。** 在原会话里把任务分给同项目成员。正式写入成功后返回任务编号，发送结果单独记录，方便核对。
- **每天只看项目重点。** 群简报汇总重要任务、风险和关键变更，支持预览后按项目定时发布。
- **权限和运行状态看得见。** 工作台集中呈现飞书登录、组织同步、项目角色、运行记录和审计。Agent 工具调用按工作授权约束。

## 体验原型

下载项目后，保留 `design/prototype/` 的目录结构，在浏览器打开以下文件。演示使用本地数据，体验时无需飞书凭据或后端服务。

- [工作台](design/prototype/omni-relay-workbench.html) · 查看项目、任务、运行记录和系统管理。
- [飞书登录演示](design/prototype/omni-relay-workbench.html#/login) · 体验模拟授权流程。
- [飞书 Bot 卡片](design/prototype/omni-relay-feishu-prototype.html) · 试用任务查询、任务分派和项目简报三种交互。
- [色彩系统](design/prototype/omni-relay-color-system.html) · 查看浅色、深色主题及设计色值。

## 运行工作台

工作台由 Go 服务同源提供。准备支持 TLS 的 PostgreSQL 和飞书自建应用，参照[环境变量模板](config/backend.env.example)向服务注入凭据。将 HTTPS 入口设置为 `OMNIRELAY_PUBLIC_ORIGIN`，并在飞书应用内登记该地址下的 `/auth/feishu/callback`。首次启动需要本应用下首位管理员的飞书 Open ID。

使用账号密码登录时，设置 `OMNIRELAY_TLS_CERT_FILE` 和 `OMNIRELAY_TLS_KEY_FILE`，让 Go 监听 HTTPS。若经 Kubernetes Ingress 访问，入口到 Go 服务的连接也需使用 TLS，并验证上游证书。只在入口终止 TLS 的部署仍可提供飞书登录。避免在入口访问日志及 APM 中采集 `/auth/` 请求体或登录回调查询参数。

```sh
mkdir -p bin
go build -o bin/omnirelay ./cmd/omnirelay
./bin/omnirelay migrate
./bin/omnirelay serve
```

同步通讯录后，管理员可在「系统管理 → 用户管理」开通目录人员。账号密码登录需要运维为现有有效账号单独设置密码。命令从终端无回显地读取至少 15 个字符且最多 256 字节的密码，设置或重置密码会撤销该账号的现有会话。

```sh
./bin/omnirelay password <account-id> <username>
```

已开通且启用的账号自动拥有普通用户角色，可登录并维护本人资料。系统管理员可管理账号、系统角色和 Bot 配置，并执行目录同步和连接检测。系统运维者可查看用户管理、角色权限、审计日志与 Bot 配置，并执行目录同步和连接检测。审计查看者可查看审计日志。多角色权限取并集，系统角色按租户和应用隔离。

系统角色授权、撤销和用户启停经服务端鉴权并写入审计。项目级权限由可信的 OmniGrid 正式成员关系、业务权限和委派代次决定，当前运行配置尚未接通项目权限源。接口字段见[权限能力协议](design/engineering/feishu-integration-contract.md#系统角色与权限能力)。

## 系统架构

### 整体拓扑

```mermaid
flowchart TB
    Chat["飞书群聊与私聊"]
    Web["Web 工作台"]
    PG[("PostgreSQL<br/>账号、队列与回执")]
    Business["OmniGrid Business API<br/>正式项目与任务"]

    subgraph K8s["Kubernetes 应用层"]
        Go["OmniRelay Go 服务<br/>飞书接入、权限、编排与交付"]
        DSH["DeepSeek Harness<br/>独立 Agent 运行时"]
    end

    Chat <-->|"消息与卡片"| Go
    Web <-->|"登录与管理"| Go
    Go -->|"执行请求"| DSH
    DSH -->|"受限工具回调"| Go
    Go <-->|"保存状态"| PG
    Go <-->|"按成员与项目范围访问"| Business
```

### 从飞书交办到正式任务

```mermaid
sequenceDiagram
    participant F as 飞书
    participant G as OmniRelay Go
    participant D as DeepSeek Harness
    participant B as OmniGrid

    F->>G: 投递成员的派单消息
    G->>G: 去重入队，核验成员和项目
    G->>D: 运行分派 Agent
    D->>G: 调用受限派单工具
    G->>B: 按实际发起人提交任务
    B-->>G: 返回正式任务编号和回执
    G->>G: 保存工作结果与待发回复
    G-->>F: 回复原会话
```

[模块设计](design/specs/omni-relay-module-design.md) · [Go 服务设计](design/specs/omni-relay-go-service-design.md) · [Kubernetes 部署设计](design/specs/omni-relay-k8s-deployment-design.md) · [UI 规范](design/specs/omni-relay-ui-standards.md)

## 项目目录

```text
design/                可体验原型、架构设计和 UI 规范
cmd/omnirelay/         Go 服务入口
internal/              Go 业务、接入和存储模块
web/                   正式工作台
runtime/dsh/           DSH 运行配置与工具桥
migrations/            PostgreSQL 迁移
config/                配置模板
deploy/k8s/            Kubernetes 部署
tests/                 契约测试、集成测试和样例
```

[原型与设计索引](design/README.md)汇总可体验页面和设计资料。

## 参与讨论

欢迎分享团队在飞书中查进度、派任务和发简报的实际流程，也欢迎对原型、权限模型和业务接口提出反馈。

## 开源项目与致谢

**DeepSeek** 为 OmniRelay 的 Agent 运行与编排设计提供了两个重要参考。

| 项目标识 | 项目仓库 | 在 OmniRelay 中的用途 |
| :---: | --- | --- |
| [官方原图](https://github.com/deepseek-ai/DeepSeek-LLM/blob/main/images/logo.svg) | [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) | 架构采用的独立 Agent 运行时 |
| [官方原图](https://github.com/deepseek-ai/DeepSeek-LLM/blob/main/images/logo.svg) | [DeepSeek SuperAgent 研究原型](https://github.com/deepseek-ai/dsh-superagent-prototype) | 工作编排、授权与执行恢复的设计参考 |
| <a href="https://github.com/larksuite"><img src="https://avatars.githubusercontent.com/u/54944174?v=4" alt="Lark 官方 GitHub 组织头像" height="30"></a> | [飞书 Go SDK](https://github.com/larksuite/oapi-sdk-go) | 飞书消息与 Bot 接入的 SDK 选型 |
| <a href="https://www.postgresql.org/media/img/about/press/elephant.png"><img src="https://www.postgresql.org/media/img/about/press/elephant.png" alt="PostgreSQL 官方 Logo" height="30"></a> | [PostgreSQL 源码镜像](https://github.com/postgres/postgres) | 工作队列和运行账本的数据库选型 |
| <a href="https://github.com/cncf/artwork/tree/main/projects/kubernetes"><img src="https://raw.githubusercontent.com/cncf/artwork/main/projects/kubernetes/icon/color/kubernetes-icon-color.svg" alt="Kubernetes 官方 Logo" height="30"></a> | [Kubernetes](https://github.com/kubernetes/kubernetes) | 部署架构所用的容器编排平台 |

[DeepSeek 品牌素材使用指引](https://github.com/deepseek-ai/awesome-deepseek-integration/blob/main/docs/_logo%20svg/guidelines.md)

当前原型带有 [Quill](https://github.com/slab/quill)、[markdown-it](https://github.com/markdown-it/markdown-it) 和 [highlight.js](https://github.com/highlightjs/highlight.js) 的浏览器分发文件。

<a href="https://github.com/slab/quill"><img src="https://raw.githubusercontent.com/quilljs/quill/v2.0.3/packages/website/public/assets/images/logo.svg" alt="Quill Logo" width="76"></a>

## 许可证

OmniRelay 采用 [MIT License](LICENSE)。原型附带的第三方代码保留各自的许可证。 [第三方许可证与来源](THIRD_PARTY_NOTICES.md)
