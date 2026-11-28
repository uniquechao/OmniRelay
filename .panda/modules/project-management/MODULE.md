# Project Management

## 职责边界

OmniRelay 使用本地 PostgreSQL 管理项目档案、阶段和计划日期。工作台从项目目录进入项目概览，项目角色和项目档案权限由当前账号与项目成员关系共同决定。

## 关联源码路径

- `internal/project/directory.go`、`internal/project/profile.go`、`internal/project/http.go` 提供目录、概览和项目档案写接口。
- `internal/access/roles.go` 和 `internal/access/local_projects.go` 提供创建权限和现行项目成员授权。
- `migrations/0021_local_project_directory.sql`、`migrations/0024_project_profile_dates.sql` 定义项目档案和计划日期，业务时区保存在 `project_task_boards`。
- `frontend/src/features/projects/ProjectListPage.tsx` 和 `ProjectMembers.tsx` 共用工作台的 React `DataList`。项目目录在服务端筛选、排序和分页，成员沿用接口返回的角色、发言时间顺序。成员的组织路径来自已发布的组织快照，读取接口按当前账号的通讯录权限返回。
- `frontend/src/features/projects/ProjectWorkspacePage.tsx` 负责项目标题、标签导航和请求生命周期。项目群聊、文件、任务看板与 Bot 分别复用其功能目录的组件。

## 长期设计约束

- 项目关注是账号的个人偏好，使用当前项目会籍持久化。列表、概览和工作台快捷入口读取同一状态。归档项目仍可关注，失去项目访问权后菜单隐藏该项目。关注操作不会改变项目版本与成员授权。

- 项目 PM 的来源权限校验、历史查询时效和已提交写入回执分别处理。设置主群只刷新本次读取的归属元数据；历史回执只能在重新授权后展示。

- 系统管理员拥有 `project.create`。创建项目时以同一事务登记档案、首位项目负责人、看板时区和审计。创建后沿用本地项目成员授权。
- 项目负责人和具备当前 `project.settings` 权限的项目成员可以维护档案。档案写入使用 `expected_version`，修改时区还需看板版本。仅有历史成员关系的项目沿用档案版本 `0` 的兼容读写。
- 归档后，项目成员保留原有的历史读取权限，可在项目目录按「已归档」查询。项目档案停止编辑，简报计划停止，未投递简报在发送前重新核对项目阶段。阶段变更、项目创建和归档均写入审计。
- 项目「文件」沿用 `#/projects/{projectID}/docs`，与全局网盘共用 `frontend/src/features/drive/DrivePage.jsx`。项目列表先按可读群来源、有效归属和本地文档读者授权取数，再统一筛选、排序和分页。网盘版本预览与下载携带 `source_project`，本地文档正文单独鉴权读取。

项目群 Agent 从可信通道取得群绑定与消息主体，按正式会籍代次、账号权限版本和群绑定版本确定工作授权。工具读取和向群回复前重验。Gateway、Runtime 的验收由内部通道完成，外部消息适配器单独验证。

项目 PM 业务工具的名称、参数与动作由 `internal/agent/pmtools` 定义。Go 在绑定工作的事务内按真实账号执行，写入和回执原子提交。群内提问直接回复原消息或原话题。群内任务清单查正式看板，在主群或唯一绑定群展示未完成事项；附属群以本群可核验的消息为限。只有发起人明确要求提醒，才解析正式负责人并在原群生成飞书原生 @。使用群外来源的答复保存在发起人的项目工作台。来源受限的结果在发布和历史展示前复核。逐工具验收见 [项目 PM 业务工具与权限设计](../../../design/engineering/project-pm-tool-capabilities.md)。

已认证的当轮请求由模型判定读取、起草、修订、确认或执行意图。Gateway 将操作限制在具体对象和当前权限内。个人会话中可确认的草稿保存用户看到的内容及目标版本；确认消费、业务写入与回执在同一事务中完成。工作台从提交回执生成简报详情链接，运行记录分别展示业务提交和回复投递。

私聊身份查询使用发件人的已启用账号和全局查询助手，会话与项目群隔离。主动项目提醒由具备 `project.settings` 权限的项目成员登记。Go 固化项目、群或个人地址和相应的授权版本，Harness 持有原生定时器。每次触发按登记目标分别去重，取数和发送各自重新验证授权。群提醒的工具取数限于项目，个人提醒使用收件人本人的字段。

## 接口与测试

- 项目 Bot 按需加载项目对话组件，使用 DSH 的公开消息和 Markdown 组件、品牌图形与样式。工作台从已授权的站内接口校验消息，自主管理草稿、发送、重试及撤权后清理。升级 DSH 时联验输入、主题与消息协议。
- 项目概览与 Bot 共用工作台的 1753px 最大宽度和居中规则。项目标题与标签使用同一左边线，空对话从内容区左上方排起，有历史时输入框停靠底部。
- 工作台项目 Bot 通过 `GET/POST /api/v1/projects/{projectID}/bot/messages` 读写本人的项目对话。Gateway 从登录会话和有效会籍确定身份与项目，Gateway 在当前账号、项目、会话、角色和授权范围内为每条个人请求提供近期已展示且来源仍可读的上下文。DSH 按工作项运行独立原生会话，重试复用同一工作项。模型返回后，Gateway 再次核验工具回执与当前权限并生成项目署名。飞书接入管理位于 Bot 页的「飞书接入」入口。
- `GET /api/v1/projects` 返回 `can_create`。`GET /api/v1/projects/{projectID}/overview` 返回 `can_manage_profile` 与档案版本。`POST /api/v1/projects` 支持以 `Idempotency-Key` 重试创建。档案修改和归档分别使用 `PUT /api/v1/projects/{projectID}/profile` 与 `POST /api/v1/projects/{projectID}/archive`。
- `GET /api/v1/projects/{projectID}/files` 提供项目文件列表，`GET /api/v1/projects/{projectID}/files/documents/{sourceID}` 读取本地文档正文。
- 当用户明确要求把刚才的只读分析整理入库，Gateway 绑定当前提问受理时已展示的上一轮答复，重新检查当时的项目、角色、来源版本与权限。历史答复仅供整理内容，本轮原话决定可执行的工具及目标。普通“确认写入”沿用已展示草稿的逐项确认。
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
last_used_ts: 1795349556499
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

项目简报按租户、应用、项目和业务日期唯一。PM 插件按版本写入成员可见的标题和正文。概览和简报列表按业务日期倒序，历史生成记录按其原始授权展示，群投递使用单独回执。会议纪要的手动标记按项目和来源引用保存，展示时重新检查当前成员的来源阅读权。

PM 的 `briefing_list` 按业务日期列出已撰写简报的当前版本，`briefing_get` 读取正文。已发布日报统计通过 `daily_report_query` 的 published 来源查投递消息。群历史检索使用与看板一致的项目时区，按含首尾业务日的日期范围分页；结果只覆盖本人可读的索引文本。

- 工作台 `#/projects/{id}/tasks` 挂载独立 React 看板，按当前登录会话和会籍读取任务。列表视图使用 `tasks?task_layout=list`，筛选参数和布局按项目保存。看板、独立任务链接 `tasks/{taskID}` 和 `tasks/new` 均挂载 ProjectTaskInspector，共用里程碑、附件、并发编辑及删除操作。
- 项目简称在全库唯一且在项目改名后保留。用户可见任务编号由三位项目简称、`-T` 和至少三位项目内序号组成，API 的 task_code 用于展示和搜索，详情及关联仍使用原任务主键。
- 业务阶段与展示列分开，修改设置按版本校验，在同一事务中迁移整列任务。看板的列计数反映筛选后的全量，续页游标绑定筛选条件、项目、列和版本。
- 列表通过 `tasks/query` 在整个筛选结果内排序和分页。“全部”包含已取消任务。认领由服务端根据当前会籍确定负责人，仅开放且未分配的任务可认领，同一次认领重试复用回执。
- 候选依据使用项目已登记来源，审核操作重新读取来源权限和修订。确认创建正式任务，合并只增加来源依据。相关回归见 `TestTaskBoardReplicaBrowser`。
- 任务详情与里程碑的任务投影共用互动成员查询。参与人界面按成员 ID 合并手动选择与已提交的活动、反馈；权限判定沿用明确指派。标签选择和参与人多选使用与详情抽屉一致的弹层组件。

- 新版抽屉由具有任务分派权的成员带原因确认删除。取消与重开沿用领域层的变更原因校验。项目经理工具的任务能力从 Gateway 目录动态注册，写回执绑定工作项和调用槽，来源授权在交付前重读。

任务说明和验收条件保存 Markdown 原文，附件与任务同事务创建，读取约束到所属项目及有效任务。

手动添加成员通过系统人员搜索下拉查询启用账号与已发布的飞书通讯录，排除已入项、停用和离职人员。群聊归属和手动搜索范围相互独立。

绑定群时用首份有效群名单初始化普通项目成员。已有绑定在升级时补齐，多群重叠按账号去重；已有角色及明确移除的记录优先。此后由项目管理员维护正式会籍。任务负责人和参与人仍可选已绑定群的有效成员，单独指派任务不授予项目访问权。

项目成员列表按有效项目角色排序，项目管理员始终在前。同一角色内按查看者可见的已绑定项目群中最近发言时间倒序，无发言在组末。发言时间取未撤回人员消息的发送时间，时间相同按成员 ID 稳定排列。

项目 Bot 的 SDK 和系统管理员原生 DSH Web 共享 Runtime Pod 内的 Agent 会话存储及模型 Secret。工作台校验浏览器登录与 `dsh.debug`；进入后使用 DSH owner 权限。SDK 和 Web 通过原生会话锁串行写入。管理员可在会话中执行 `/handoff` 交还空闲写入权。

预览环境由 `preview-project-bot` 调用 `preview-dsh-runtime`，Runtime 归属使用内部 Service 主机名。正式 `dshbot` 控制台运行在 `omnirelay-dsh-prod` 的 `console` 容器。工作负载与数据卷映射见[部署说明](../../../deploy/k8s/README.md#预览-bot-与-dsh-runtime)。

个人 Bot 对话的历史依据由 Gateway 保存。每个工作使用独立原生会话，只向相同账号、渠道、话题、项目、角色和授权代次投影已复核的最近问答；派发、工具写入和展示再次检查来源。已展示草案按工作和条目确认，正式业务写入与用户回执同事务结算。
