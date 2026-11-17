# OmniRelay

<img src="design/prototype/omni-relay-logo-bicolor.png" alt="OmniRelay Logo" width="280">

**在飞书里查项目进度、分派任务、看每日简报。**

OmniRelay 是一个面向小型团队和组织的飞书项目助手。它把群聊里的项目问题和任务交办接到正式项目系统，让成员在原会话里看到进度、负责人、截止时间和操作回执。首期以 **500 人以内的单飞书组织、自托管部署**为目标。

**当前处于交互原型与架构设计阶段。** 仓库提供可离线体验的演示和技术设计。真实飞书接入、Agent 后端和部署清单会随实现补齐。

![OmniRelay 工作台示例，展示项目进展、关键任务和阻塞事项](design/prototype/omni-relay-workbench-preview.png)

## 想解决的场景

| 在飞书里的需求 | OmniRelay 的设计 |
| --- | --- |
| 「这个项目今天还有哪些任务卡住了？」 | 查询你有权限查看的共享任务，返回负责人、日期、状态和来源。群内回复同时遵循该群的公开范围 |
| 「建个联调任务，分给王宁。」 | 核验项目和成员，明确交办后创建任务并返回正式编号 |
| 「每天让项目群知道哪些事要优先处理。」 | 汇总重要任务、风险和变更，可先预览，再按项目计划发布 |

工作台还规划了飞书登录、部门与人员同步、角色权限和运行审计，方便团队管理接入范围及排查任务和消息状态。

## 体验原型

下载项目后，保留 `design/prototype/` 的目录结构，在浏览器打开以下文件。演示使用本地数据，体验时无需飞书凭据或后端服务。

- [工作台](design/prototype/omni-relay-workbench.html) · 查看项目、任务、运行记录和系统管理。
- [飞书登录演示](design/prototype/omni-relay-workbench.html#/login) · 体验模拟授权流程。
- [飞书 Bot 卡片](design/prototype/omni-relay-feishu-prototype.html) · 试用任务查询、任务分派和项目简报三种交互。
- [色彩系统](design/prototype/omni-relay-color-system.html) · 查看浅色、深色主题及设计色值。

## 计划架构

**Go** 承担飞书接入、权限校验、工作编排和消息交付。**DeepSeek Harness（DSH）** 在独立运行环境里执行 Agent，通过受限工具访问业务。**PostgreSQL** 保存工作队列、账号、运行记录和回执。首期业务数据对接 **OmniGrid Business API**，部署目标为 **Kubernetes**。

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

[原型与设计索引](design/README.md)汇总可体验页面和设计资料。其余工程目录为后续实现预留。

## 路线图

1. 在测试飞书群验证 Bot、Go、DSH 和 PostgreSQL 的只读链路。
2. 接入飞书登录、通讯录同步和正式任务数据，验证成员与项目权限。
3. 实现任务分派、操作回执与故障恢复。
4. 支持每日简报预览和定时发布，完成 Kubernetes 部署与恢复验证。

欢迎对交互原型、权限模型和业务接口提出反馈。

## 开源项目与致谢

**DeepSeek** 为 OmniRelay 的 Agent 运行与编排设计提供了两个重要参考。

| 项目仓库 | 在 OmniRelay 中的用途 |
| --- | --- |
| [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) | 计划接入的独立 Agent 运行时 |
| [DeepSeek SuperAgent 研究原型](https://github.com/deepseek-ai/dsh-superagent-prototype) | 工作编排、授权与执行恢复的设计参考 |
| [飞书 Go SDK](https://github.com/larksuite/oapi-sdk-go) | 计划用于 Go 服务的飞书消息与 Bot 接入 |
| [PostgreSQL 源码镜像](https://github.com/postgres/postgres) · [官方 Logo](https://www.postgresql.org/media/img/about/press/elephant.png) | 计划用于工作队列和运行账本 |
| [Kubernetes](https://github.com/kubernetes/kubernetes) · [官方 Logo](https://github.com/cncf/artwork/tree/main/projects/kubernetes) | 计划部署平台 |

[DeepSeek 官方 Logo](https://github.com/deepseek-ai/DeepSeek-LLM/blob/main/images/logo.svg) · [品牌素材使用指引](https://github.com/deepseek-ai/awesome-deepseek-integration/blob/main/docs/_logo%20svg/guidelines.md)

当前原型带有 [Quill](https://github.com/slab/quill)、[markdown-it](https://github.com/markdown-it/markdown-it) 和 [highlight.js](https://github.com/highlightjs/highlight.js) 的浏览器分发文件。

<a href="https://github.com/slab/quill"><img src="https://raw.githubusercontent.com/quilljs/quill/v2.0.3/packages/website/public/assets/images/logo.svg" alt="Quill Logo" width="76"></a>

## 许可证

OmniRelay 采用 [MIT License](LICENSE)。原型附带的第三方代码保留各自的许可证。 [第三方许可证与来源](THIRD_PARTY_NOTICES.md)
