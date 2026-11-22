# Project Management

## 职责边界

OmniRelay 使用本地 PostgreSQL 管理项目档案、阶段和计划日期。工作台从项目目录进入项目概览，项目角色和项目档案权限由当前账号与项目成员关系共同决定。

## 关联源码路径

- `internal/project/directory.go`、`internal/project/profile.go`、`internal/project/http.go` 提供目录、概览和项目档案写接口。
- `internal/access/roles.go` 和 `internal/access/local_projects.go` 提供创建权限和现行项目成员授权。
- `migrations/0021_local_project_directory.sql`、`migrations/0024_project_profile_dates.sql` 定义项目档案和计划日期，业务时区保存在 `project_task_boards`。
- `web/projects.js`、`web/project-profile.js` 提供列表、概览和项目设置抽屉，由 React 工作台的业务页适配层挂载。

## 长期设计约束

- 系统管理员拥有 `project.create`。创建项目时以同一事务登记档案、首位项目负责人、看板时区和审计。创建后沿用本地项目成员授权。
- 项目负责人和具备当前 `project.settings` 权限的项目成员可以维护档案。档案写入使用 `expected_version`，修改时区还需看板版本。仅有历史成员关系的项目沿用档案版本 `0` 的兼容读写。
- 归档后，项目成员保留原有的历史读取权限，可在项目目录按「已归档」查询。项目档案停止编辑，简报计划停止，未投递简报在发送前重新核对项目阶段。阶段变更、项目创建和归档均写入审计。
- 项目「文件」沿用 `#/projects/{projectID}/docs`，与全局网盘共用 `web/drive.js`。项目列表先按可读群来源、有效归属和本地文档读者授权取数，再统一筛选、排序和分页。网盘版本预览与下载携带 `source_project`，本地文档正文单独鉴权读取。

项目群 Agent 从可信通道取得群绑定与消息主体，按正式会籍代次、账号权限版本和群绑定版本确定工作授权。工具读取和向群回复前重验。Gateway、Runtime 的验收由内部通道完成，外部消息适配器单独验证。

项目 PM 业务工具的名称、参数与动作由 `internal/agent/pmtools` 定义。Go 在绑定工作的事务内按真实账号执行，写入和回执原子提交。群内业务请求的正文仅进入发起人的项目工作台，原话题接收署名提示。来源受限的结果在发布和历史展示前复核。逐工具验收见 [项目 PM 业务工具与权限设计](../../../design/engineering/project-pm-tool-capabilities.md)。

私聊身份查询使用发件人的已启用账号和全局查询助手，会话与项目群隔离。主动项目提醒由具备 `project.settings` 权限的项目成员登记。Go 固化项目、群或个人地址和相应的授权版本，Harness 持有原生定时器。每次触发按登记目标分别去重，取数和发送各自重新验证授权。群提醒的工具取数限于项目，个人提醒使用收件人本人的字段。

## 接口与测试

- 项目 Bot 按需挂载与 Runtime 版本对应的 DSH 官方会话容器、输入栏和消息组件，工作台负责对接已授权的站内消息。升级 Runtime 时同步核对前端组件与输入、主题及消息协议。
- 工作台项目 Bot 通过 `GET/POST /api/v1/projects/{projectID}/bot/messages` 读写本人的项目对话。Gateway 从登录会话和有效会籍确定身份与项目，DSH 按成员及授权代次续接会话。模型返回后，Gateway 再次核验工具回执与当前权限并生成项目署名。飞书接入管理位于 Bot 页的「飞书接入」入口。
- `GET /api/v1/projects` 返回 `can_create`。`GET /api/v1/projects/{projectID}/overview` 返回 `can_manage_profile` 与档案版本。`POST /api/v1/projects` 支持以 `Idempotency-Key` 重试创建。档案修改和归档分别使用 `PUT /api/v1/projects/{projectID}/profile` 与 `POST /api/v1/projects/{projectID}/archive`。
- `GET /api/v1/projects/{projectID}/files` 提供项目文件列表，`GET /api/v1/projects/{projectID}/files/documents/{sourceID}` 读取本地文档正文。
- `internal/project/profile_test.go` 覆盖 PostgreSQL 项目生命周期，`tests/integration/project_workbench_browser.test.mjs` 覆盖工作台操作和冲突恢复。

<!-- panda:module-meta:start -->
---
sync_uid: 01a1fa0a-a10b-7407-8788-00d3095544b5
module_id: 81
project_id: 25
slug: project-management
display_name: "Project Management"
agent: codex
source: auto
status: active
created_ts: 1795196363019
last_used_ts: 1795196363110
---
<!-- panda:module-meta:end -->
