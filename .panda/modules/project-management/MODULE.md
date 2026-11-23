# Project Management

## 职责边界

OmniRelay 使用本地 PostgreSQL 管理项目档案、阶段和计划日期。工作台从项目目录进入项目概览，项目角色和项目档案权限由当前账号与项目成员关系共同决定。

## 关联源码路径

- `internal/project/directory.go`、`internal/project/profile.go`、`internal/project/http.go` 提供目录、概览和项目档案写接口。
- `internal/access/roles.go` 和 `internal/access/local_projects.go` 提供创建权限和现行项目成员授权。
- `migrations/0021_local_project_directory.sql`、`migrations/0024_project_profile_dates.sql` 定义项目档案和计划日期，业务时区保存在 `project_task_boards`。
- `web/projects.js`、`web/project-profile.js` 提供列表、概览和项目设置抽屉，由 React 工作台的业务页适配层挂载。

## 长期设计约束

- 项目 PM 的来源权限校验、历史查询时效和已提交写入回执分别处理。设置主群只刷新本次读取的归属元数据；历史回执只能在重新授权后展示。

- 系统管理员拥有 `project.create`。创建项目时以同一事务登记档案、首位项目负责人、看板时区和审计。创建后沿用本地项目成员授权。
- 项目负责人和具备当前 `project.settings` 权限的项目成员可以维护档案。档案写入使用 `expected_version`，修改时区还需看板版本。仅有历史成员关系的项目沿用档案版本 `0` 的兼容读写。
- 归档后，项目成员保留原有的历史读取权限，可在项目目录按「已归档」查询。项目档案停止编辑，简报计划停止，未投递简报在发送前重新核对项目阶段。阶段变更、项目创建和归档均写入审计。
- 项目「文件」沿用 `#/projects/{projectID}/docs`，与全局网盘共用 `web/drive.js`。项目列表先按可读群来源、有效归属和本地文档读者授权取数，再统一筛选、排序和分页。网盘版本预览与下载携带 `source_project`，本地文档正文单独鉴权读取。

项目群 Agent 从可信通道取得群绑定与消息主体，按正式会籍代次、账号权限版本和群绑定版本确定工作授权。工具读取和向群回复前重验。Gateway、Runtime 的验收由内部通道完成，外部消息适配器单独验证。

项目 PM 业务工具的名称、参数与动作由 `internal/agent/pmtools` 定义。Go 在绑定工作的事务内按真实账号执行，写入和回执原子提交。群内业务请求的正文仅进入发起人的项目工作台，原话题接收署名提示。来源受限的结果在发布和历史展示前复核。逐工具验收见 [项目 PM 业务工具与权限设计](../../../design/engineering/project-pm-tool-capabilities.md)。

私聊身份查询使用发件人的已启用账号和全局查询助手，会话与项目群隔离。主动项目提醒由具备 `project.settings` 权限的项目成员登记。Go 固化项目、群或个人地址和相应的授权版本，Harness 持有原生定时器。每次触发按登记目标分别去重，取数和发送各自重新验证授权。群提醒的工具取数限于项目，个人提醒使用收件人本人的字段。

## 接口与测试

- 项目 Bot 按需挂载与 Runtime 版本对应的 DSH 官方会话容器、输入栏和消息组件，工作台负责对接已授权的站内消息。升级 Runtime 时同步核对前端组件与输入、主题及消息协议。
- 项目概览与 Bot 共用工作台的 1753px 最大宽度和居中规则。项目标题与标签使用同一左边线，空对话从内容区左上方排起，有历史时输入框停靠底部。
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

## 成员资料与任务删除

- 项目成员页面的 `project_role` 取系统角色目录的 project scope，显示项目管理员和项目成员；管理员授予与撤销复用系统角色命令、权限版本及会话失效。`role` 保留旧会籍权限语义，`role_title` 仅保留兼容数据；`responsibility` 为可编辑项目职责。资料版本 `version` 与会籍代次 `generation` 分开，canonical 角色修改同时校验 `expected_version` 和 `expected_project_role`，防止旧草稿覆盖系统角色页的新授权。
- 成员候选来自当前租户、应用的启用系统账号，空查询可浏览，支持姓名及登录账号搜索，排除已入项人员。任务负责人和参与人复用同一用户展示与搜索组件，限定当前项目的有效成员，写入仍使用 member_id。插件成员命令接受同一项目角色目录，保留旧访问角色兼容。
- 项目设置权限可搜索启用的候选账号并管理普通成员，负责人任免另验委派权限。移除最后一位有效负责人或仍负责/参与未完成任务的成员会冲突；需先通过正式任务命令交接。成员变更和审计同事务提交。
- `DELETE /api/v1/projects/{projectID}/tasks/{taskID}` 要求任务管理权、`expected_version` 和 `change_reason`；`request_key` 可重放原回执。任务逻辑删除保留活动和来源引用，但退出看板、搜索、详情、评论和来源任务查询，同时解除节点绑定。插件 `task_delete` 共用同一命令，由 Gateway 回执账本去重。
- 插件 `member_candidates`、`member_update` 分别提供候选查询和按资料版本修改；`task_create` / `task_update` 接受日期、验收条件、里程碑、参与人与标签，仍执行字段级权限。
- 关键回归：`TestMemberTaskCRUDAtomicity`、`TestProjectMemberTaskCRUDBrowser`、`TestProjectPMToolMatrix`、`TestProjectToolCRUDRecoveryGate`。浏览器通过真实登录与独立 PostgreSQL 验证网页操作；Gateway 回调独立验证插件重试和撤权。

## 项目任务看板

- 工作台 `#/projects/{id}/tasks` 挂载独立 React 看板，按当前登录会话和会籍读取任务。旧列表书签 `tasks/list` 转到同一工作区的列表视图，保留搜索、负责人及里程碑筛选。单任务管理入口 `tasks/{taskID}` 支持里程碑及软删除。
- 任务编号在项目内稳定。业务阶段与展示列分开，修改设置按版本校验，在同一事务中迁移整列任务。看板的列计数反映筛选后的全量，续页游标绑定筛选条件、项目、列和版本。
- 候选依据使用项目已登记来源，审核操作重新读取来源权限和修订。确认创建正式任务，合并只增加来源依据。相关回归见 `TestTaskBoardReplicaBrowser`。
- 任务详情与里程碑的任务投影共用互动成员查询。参与人界面按成员 ID 合并手动选择与已提交的活动、反馈；权限判定沿用明确指派。标签选择和参与人多选使用与详情抽屉一致的弹层组件。

- 新版抽屉由具有任务分派权的成员带原因确认删除。取消与重开沿用领域层的变更原因校验。项目经理工具的任务能力从 Gateway 目录动态注册，写回执绑定工作项和调用槽，来源授权在交付前重读。
