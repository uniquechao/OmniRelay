# 飞书接入目标契约与样例

依据：[接入设计 §2–5](../specs/omni-relay-feishu-integration.md)、[工程标准](feishu-integration-standards.md)。本条负责固定供前后端复用的目标协议，不包含 Bot 领域、OAuth 修补或目录改造。下文标记“实施约定”的内容用于补齐设计留白；其余保留设计规定值。

Go 服务已接入 Bot 管理接口与 OAuth 固定错误回跳。目录响应字段按 `internal/directory/http.go` 固定。

## 系统角色与权限能力

`GET /api/v1/me` 保留 `principal`、`csrf_token`、`access.roles` 和 `access.admin`。有效系统角色使用字符串数组，启用账号自动包含 `system_user`。`access.admin` 提供 `users`、`roles`、`audit` 和 `feishu_bot` 菜单布尔值。

`access.permissions` 将操作标识映射到布尔值，未知或缺省权限按拒绝处理。`access.navigation` 提供 `workbench` 和 `system` 一级导航布尔值。`access.default_route` 是白名单内的站内路径。多角色的默认路径优先级依次为系统管理员 `/#/workbench`、系统运维者 `/#/system/users`、审计查看者 `/#/system/audit`、普通用户 `/#/workbench`。OAuth 优先保留有权访问的白名单返回路径。`access.permission_version` 是十进制字符串，授权、撤权或账号启停成功时递增。

系统运维者在没有系统管理员角色时默认仅显示系统管理。项目权限源确认有正式项目访问权限后同时显示工作台。

| 权限 | 系统管理员 | 系统运维者 | 审计查看者 | 普通用户 |
| --- | --- | --- | --- | --- |
| `account.read`、`role.read`、`directory.read`、`directory.sync`、`system.read`、`system.operate` | 允许 | 允许 | 拒绝 | 拒绝 |
| `account.manage`、`role.manage`、`system.configure` | 允许 | 拒绝 | 拒绝 | 拒绝 |
| `audit.read` | 允许 | 允许 | 允许 | 拒绝 |

`GET /api/v1/system/roles` 要求 `role.read`，返回 `items` 数组。每个角色条目包含 `id`、`name`、`scope`（`system` 或 `project`）、`default` 和 `grantable` 布尔值以及中文说明字符串数组 `permissions`。账号接口的 `systemRoles` 为有效系统角色字符串数组，停用账号返回 `[]`。默认普通用户由启用状态计算，现有授权记录仅包含真实授权。项目权限继续由可信项目成员关系和业务能力计算。

旧客户端可以继续读取 `principal`、`csrf_token`、`access.roles`、`access.admin`、原账号字段和授权列表。以下为运维账号的 `access` 和账号项的相关字段示例。

```json
{
  "roles": ["system_operator", "system_user"],
  "admin": {"users": true, "roles": true, "audit": true, "feishu_bot": true},
  "permissions": {
    "account.read": true,
    "account.manage": false,
    "role.read": true,
    "role.manage": false,
    "directory.read": true,
    "directory.sync": true,
    "system.read": true,
    "system.configure": false,
    "system.operate": true,
    "audit.read": true
  },
  "navigation": {"workbench": false, "system": true},
  "default_route": "/#/system/users",
  "permission_version": "7"
}
```

```json
{"id":"account_example","active":true,"systemRoles":["system_operator","system_user"]}
```

系统角色授予成功后，仍在使用的会话可从 `/me` 取得新能力。撤权或停用使旧会话下一次请求返回 401。已登录但缺少操作点时返回 403，调用者无法读取的授权记录返回 404。并发操作会重新检查最后管理员约束，触发保护时返回 409。

## 文件与运行方式

在项目根执行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests/contract -p 'test_*.py' -v`，只需 Python 3 标准库，不联网、不用数据库、不跳过测试。

| 文件（均位于 `tests/fixtures/feishu/`） | 用途 |
| --- | --- |
| `bot-config.json` | GET/PATCH 成功完整响应；初始 0/关闭、5 种连接状态、ETag、无收信时间 |
| `bot-checks.json` | GET 检测任务；4 种任务状态、3 种检测项状态、4 种脱敏错误 |
| `check-submissions.json` | POST 新任务、同键重试、异键合并、终态重试 |
| `idempotency-sequences.json` | 活动任务、终态、保留期限与凭据换代的连续场景 |
| `http-matrix.json` | 逐行独立的 HTTP 正反例，初始配置版本固定为 0、目录版本为 17 |
| `oauth.json` | 3 种新错误、旧 feishu 兼容及 state 拒绝场景 |
| `directory.json` | 部门/人员/历史列表的同版快照、初次无快照、失败保留与撤权隐藏 |
| `directory-submissions.json` | 扁平同步任务、coalesced 正反例及全部运行状态 |

JSON 内的 `name` 是样例标签。`status/headers/body` 是 HTTP 外层封套，不属于响应正文。目录组合样例的 `departments/people/runs` 分别是三个 GET 正文；不是一个新接口。所有标识均为虚构值；权限、会话、CSRF 在矩阵中用状态标签表达，真实适配器须以隔离测试账号生成，禁止把标签当作实际头值。

## 读取、修改与连接状态

- GET `/api/v1/system/feishu-bot`：`system.read`，返回 200 和强 ETag `"bot-<version>"`。只读存储，轮询不访问飞书。未初始化 `version=0`、`enabled=false`。
- PATCH 同地址：`system.configure`，仅接受 `{"enabled":boolean}`，成功 200，正文同 GET；每次重新鉴权并校验同源 Origin 与 CSRF。开关改变、版本递增与审计在同一事务；相同值成功但版本和变更审计保持不变。
- `If-Match` 缺失 428；过期 412。实施约定：仅接受与当前版本完全相同的一个强 ETag，弱 ETag、`*`、多值或格式异常均 412，不能绕过版本控制。缺少 enabled、非布尔或未知字段 400。
- 连接枚举固定 `disabled/connecting/connected/reconnecting/unavailable`。响应不新增连接版本字段；底层必须按配置版本、连接代次及当前持有者校验状态有效性。
- 实施约定：持有者每 10s 写心跳，`OMNIRELAY_FEISHU_BOT_HEARTBEAT_TTL=30s`；以数据库时间计算 `now-last_heartbeat_at >= TTL` 即过期。启用状态下心跳为空不能视为在线；关闭优先返回 disabled。30s 边界、旧持有者和未来时间异常均需后端测试，不使用客户端时钟。
- `last_message_at=null` 表示本次连接尚无成功持久化的合规真实事件；检测通过不能设置它。新的连接代次须清空上代收信时间。全部非空时间采用 UTC RFC 3339（以 Z 结尾）。
- `credentials_configured` 表示部署是否注入凭据，不代表凭据有效。`bot_name` 未知时为空字符串，不伪造名称。

## 检测任务与幂等

POST `/api/v1/system/feishu-bot/checks` 使用 `system.operate`，须通过 Origin 和 CSRF 校验，正文必须是空对象。202 正文包含 `id`、`state` 和 `reused`。GET `/checks/{id}` 要求 `system.read`，返回 200 和完整任务。越界应用、越界组织及不存在任务统一返回 404。

实施约定：任务 ID 是不透明非空字符串，客户端不得推导。完整任务固定字段：`id`、`state`、`credentials_generation`（十进制正整数字符串）、`created_at`、`started_at`、`finished_at`、`results`。queued 的开始/结束为 null，running 的结束为 null，终态开始/结束均非空。results 是按 `tenant_token`、`bot_info` 排列的数组；queued 为空，running 可包含已完成项，终态包含两项。项固定 `name/state/checked_at`，失败项另有 `error`，通过及跳过项不得包含 error。checked_at 为该项完成/决定跳过的时间。

状态为 `queued/running/succeeded/failed`；项状态为 `passed/failed/skipped`。租户令牌检测失败时 Bot 信息跳过；前项通过后 Bot 信息失败则整任务 failed。错误只允许 `credentials_missing/credentials_rejected/bot_unavailable/provider_temporarily_unavailable`，不能带原始响应、请求头或平台文案。旧代任务保留历史，永远不能覆盖新代诊断。

以下均为实施约定，作为后端持久化与前端重试的共同边界：

1. `Idempotency-Key` 必填，接受 1–128 字符的 `[A-Za-z0-9._:-]`；缺失、空、重复头、非法字符或过长返回 400 `invalid_idempotency_key`。前端一次点击生成一次键，网络重试沿用，用户再次主动检测生成新键。
2. 键绑定作用域为 `(tenant_key, app_id, key)`，保存绑定时的凭据代次与请求指纹。先鉴权及校验合法请求，再查键绑定，最后查活动任务。键不按账号隔离，同源合并不泄漏其他组织；每次仍要求权限。
3. 同键返回原任务，即使该任务已终态或部署已换代，返回 202、当前任务 state、`reused=true`。此返回是历史查询语义，不代表当前凭据已通过。不同键在相同 `(tenant_key, app_id, credentials_generation)` 有 queued/running 任务时绑定同一任务并返回 reused=true。新建任务才 reused=false。
4. 活动任务及所有键绑定不能因时间经过被清除。终态后至少保留 24h，固定保留到 `finished_at+24h`；重试不延长窗口。达到边界可以清理绑定，任务历史保留不得短于所有引用它的绑定。清理后原键视作新键，重新按当前代次合并/新建，前端不得依赖无限重试。
5. `OMNIRELAY_FEISHU_CREDENTIALS_GENERATION=1` 是非敏感、单调递增的部署版本字符串，轮换 App Secret 时所有副本原子更新并递增，不使用秘密值或其哈希。新键仅合并当前代次活动任务；旧键仍回原 ID。读取、领取、续租和提交必须核对代次，禁止旧代次任务更新当前状态。旧活动任务由协调/租约恢复终结为 failed，失败码为 provider_temporarily_unavailable；不新增 cancelled 检测状态。
6. 同键不同指纹返回 409 `idempotency_conflict`，不得覆盖绑定。当前合法正文只有 `{}`，非法正文先返回 400，因此该冲突主要是后续协议扩展的防护，不将非法正文改报 409。并发键绑定与活动任务唯一性依靠数据库事务，不能只用内存缓存。

部署样例在 `config/backend.env.example`。服务启动前，以独立迁移角色运行 `omnirelay migrate` 应用当前嵌入迁移，包括 `0009_directory_roster.sql`。App ID 和租户标识用于隔离数据，必须配置。缺少 App Secret 时，已有本地账号仍可管理 Bot，检测返回 `credentials_missing`，飞书登录和目录同步需要有效凭据。配置变更写入 `feishu_bot.configure` 审计事件，包含操作者、应用资源和变更版本。

数据库拒绝旧凭据代次重新启用。检测任务的领取租约为 30 秒，Worker 完成令牌阶段后续租。异常退出后，同一代次的实例可在租约过期后重新领取。配置变更及凭据换代会清除当前连接的心跳和收信时间。幂等窗口固定为 24 小时。

## HTTP、OAuth 与目录约束

| 状态 | 验收输入/含义 | 错误正文（实施约定，设计已指定者保持原值） |
| --- | --- | --- |
| 200 | 有权限 GET；管理员合法 PATCH/同值 PATCH | 对应完整正文 |
| 202 | 有权限且通过 Origin/CSRF 的检测或目录同步，含复用 | 对应提交正文 |
| 400 | 未知字段、错误类型、非法检测键、无效目录查询 | invalid_request / invalid_idempotency_key / invalid_directory_query |
| 401 | 无会话、过期或撤销会话 | unauthorized |
| 403 | 权限不足，写操作缺失/跨站 Origin、缺失/错误 CSRF | forbidden |
| 404 | 无此检测任务或跨组织/应用任务 | check_not_found |
| 409 | 快照不匹配；键指纹冲突 | directory_snapshot_changed / idempotency_conflict |
| 412 | If-Match 版本或格式不匹配 | config_version_conflict |
| 428 | 缺少 If-Match | config_version_required |

业务错误正文仅 `{"error":"<code>"}`。检测的平台失败是任务 failed，GET 仍 200；不是凭据错误就返回管理接口 401。GET 不要求 Origin/CSRF，绝不能用写接口的放行条件绕过读权限。矩阵每行单独重置基线，不按文件顺序连续执行。

OAuth：仅浏览器绑定、state 有效且原子消费成功的事务可 303 到 `/#/login?error=cancelled|denied|unavailable`。取消为 cancelled；未开通、停用、异组织为 denied；平台暂不可用为 unavailable。state 无效、过期、重复或浏览器不匹配为 400，不回跳也不建立会话。`error=feishu` 仅作为前端兼容输入样例，不再作为新后端错误目标。成功仍先请求 `/api/v1/me` 确认会话，合法内部 return_to 才能恢复；无权路由须重新核验。固定失败目标不拼接用户提供的外部 URL。

`return_to` 省略时回到 `/`，可显式指定 `/`、`/#/workbench`、`/#/system/users`、`/#/system/roles`、`/#/system/audit` 或 `/#/system/feishu-bot`。例如 `return_to=%2F%23%2Fsystem%2Ffeishu-bot` 允许返回 Bot 配置页，管理 API 仍逐次鉴权。带额外查询参数、外部主机或未经允许的路径返回 400 `invalid_return_path`。

前端在当前标签页保留白名单路由，支持取消、失败、刷新和浏览器后退后重试。固定回跳显示本地化操作提示，未知错误使用通用提示。会话确认后清除返回目标，并依据 `/api/v1/me` 的权限恢复页面或显示无权状态。显式退出清除返回目标。浏览器从后退缓存恢复时重新检查会话和权限，CSRF 仅保存在应用内存。

飞书以 `error=access_denied` 回调时使用 `cancelled`。通过事务验证后，未知授权错误、缺少授权码或换码和用户信息接口暂时不可用时使用 `unavailable`。应用诊断仅记录固定错误码。入口代理的访问日志及 APM 应使用路径模板，禁采 `/auth/feishu/callback` 查询串、OAuth 请求和响应正文、认证头与 Cookie；出站换码及用户信息请求也适用这些采集约束。

目录沿用现有扁平结构，`snapshot_version` 为正整数或 null，`visible` 为布尔。不可见时版本和 scope 为 null，部门和人员 items 必须为空，历史任务及最近成功时间可保留。people 按 open_id 去重，departments 为全部可见路径。部门及人员归属中的 path 均为从上级到当前部门的非空名称数组，例如 `["研发", "运维"]`。directory_status 为 `unknown/active/inactive/resigned`，account_status 为 `unprovisioned/active/inactive`，未开通 account_id 为 null，目录状态不会自动授予本地账号权限。

部门、人员和同步历史使用同一 snapshot_version；翻页携带，409 后丢弃混版结果重载。sync-runs POST 是扁平 run 加 coalesced，非嵌套 task；ID 为数字。初次无快照和撤权隐藏都 visible=false，靠 last_success_at 与 latest_attempt 区分。临时失败允许 visible=true 保留合法旧快照；已确认范围变化须先隐藏，后续完整发布才恢复。failure_reason 是现有接口的脱敏显示字符串，本条不引入新目录失败枚举。

部门查询省略 `parent_id` 或传 `0` 时返回可见树顶层，传实际 ID 时只返回直属子部门。人员查询省略 `department_id` 或传 `0` 时返回整个可见目录，含直接授权且无可见部门的人员。传实际 ID 时默认筛选直属人员，`include_descendants=true` 才包含下级。筛选命中后仍返回该人员的全部可见部门路径。分页排序为不区分大小写的姓名或部门名，同名以对应 ID 排序，`total` 表示筛选后、分页前的条数。

例如 `GET /departments?parent_id=0&limit=1` 得到 `snapshot_version=42` 后，可以用 `GET /people?department_id=od-example&limit=1&snapshot_version=42` 读取同版，再加 `offset=1` 读取下一页。快照被隐藏或替换后，这两个集合携带旧版本均返回 `409 {"error":"directory_snapshot_changed"}`。目录缺失数表示同范围下未再观察到的人员，范围外计数表示授权集合变化后未再观察到的人员。两项均是快照比较结果。`GET /people` 只投影当前可见快照，`resigned` 来自飞书的 `status.is_resigned=true`。缺失人员的确认结果写入持久化在册记录。

`GET /api/v1/system/accounts` 对已确认离职的账号追加 `directoryStatus: "resigned"` 和 RFC 3339 `resignedAt`。账号受最后管理员保护时追加 `disableProtected: true`。这些字段沿用 `account.read`，当前目录路径通过 `directory.read` 查询可见快照。已确认离职的停用账号在重新同步为在册前，启用请求返回 `409 {"error":"account_resigned"}`。重新同步为在册后清除这些字段，本地启用仍须管理员操作。

`GET /api/v1/system/audit-events` 中，系统触发的 `directory.resign`、`account.disable`、`account.disable_protected` 带 `directorySyncRunId`，其 `actorAccountId` 为空字符串。离职事件对象使用 `directory_person` 和现有 `person_key`，停用及保护事件对象使用本地账号 ID，保护事件结果为 `protected`。账号实际停用时记录新权限版本，同一离职事件的重复同步沿用原确认时间。

## 验证边界

离线样例校验严格字段集合、类型、枚举、时间、ETag、生命周期、目录可见性、同版与去重。设置 `OMNIRELAY_TEST_DATABASE_URL` 指向隔离 PostgreSQL 后，执行 `go test -v ./cmd/omnirelay ./internal/feishu ./migrations` 验证 Bot 管理 HTTP、适配器、任务并发与迁移。飞书适配测试为真实请求构造与响应解析注入测试传输层。预发布飞书应用、浏览器和 SDK 验收见[跨角色验收与预发布清单](feishu-integration-acceptance.md)。

### 登录浏览器回归

在项目根目录运行。浏览器测试使用 Chrome 与 `playwright-core@1.58.2`。设置 `OMNIRELAY_PLAYWRIGHT_MODULE` 指向已安装的模块，使用系统 Chrome 时设置 `OMNIRELAY_CHROME` 指向可执行文件。契约样例测试还需要 OpenSSL。联调需要将 `OMNIRELAY_TEST_DATABASE_URL` 指向支持建表的隔离 PostgreSQL。集成测试在独立 schema 中运行。

```sh
node --test --test-name-pattern='取消与失败|后退缓存|拒绝未知|密码登录' tests/integration/feishu_login_browser.test.mjs
go test -v ./cmd/omnirelay -run 'TestBackendFeishu(AdmissionAndRevocation|Browser)$'
```

联调在本地 HTTPS 中运行，飞书授权页和飞书出站响应使用测试替身。浏览器会经过实际 Go 回调、会话写入、Bot 权限检查及退出接口。可通过 `OMNIRELAY_BROWSER_SCREENSHOTS` 指定项目内目录，保存取消提示的桌面浅色与窄屏深色截图。

## Go 入站受理

`orchestrator.InboxAcceptor` 接收经过连接适配器校验的 `InboundEvent`，其中 `TenantID` 是飞书 `tenant_key`，`ChatKind` 为 `p2p` 或 `group`，`ThreadID` 和 `ReplyToMessageID` 可空。适配器使用自己的收信时间填充 `ReceivedAt`，并在该条连接建立时绑定 `ConnectionFence`。事件来源、发送者类型及群 @ 目标由适配器核验。

```go
event := orchestrator.InboundEvent{
    AppID: "cli_example", TenantID: "tenant_example",
    EventID: "evt_example", MessageID: "om_message",
    ChatID: "oc_group", ThreadID: "omt_topic", ReplyToMessageID: "om_parent",
    ChatKind: orchestrator.ChatGroup, SenderOpenID: "ou_member",
    ReceivedAt: time.Date(2026, 11, 18, 8, 22, 0, 0, time.UTC),
    Text: "查询项目进度", Connection: fence,
}
result, err := inbox.AcceptInbound(ctx, event)
```

- 连接协调器获得 Kubernetes Lease 后，通过 PostgreSQL 的 `ConnectionFence` 读取版本和代次，以该快照调用 `StartConnection` 激活并取得新代次。每条回调捕获该代次。仍持有 Kubernetes Lease 时才允许续期，确认连接健康后调用 `RenewConnection`。失去 Lease 时停止回调并用绑定的代次调用 `EndConnection`。数据库持有权会按数据库时间过期；旧代次不能续期或结束新连接。激活、结束、开关和凭据换代均清空收信时间。
- 受理先检查部署作用域与持有权，再按 `(app_id, tenant_id, message_id)` 去重。同一真实消息重投返回原 `WorkID`，保留首次受理内容和准入结论。切换代次后，由当前持有者收到历史重投时仍返回原 ID。历史重投不更新当前代次收信时间。状态中的 `last_message_at` 采用 PostgreSQL 记录本次受理的时间。
- 同组织的可信消息同时写入原始事件和工作记录。已启用账号对应 `pending_route`；未开通或停用的账号对应 `admission_denied`，工作不绑定本地账号，也不占待路由容量。两种结果都证明当前连接收到了真实消息。后续路由须重新核验账号及权限。
- 异组织、异应用或无效事件，以及关闭配置、过期版本、旧代次或过期持有权，均返回错误且不写入。待路由容量按组织和应用计算，去重后只检查新建的待路由工作。容量耗尽返回 `ErrInboxFull`，存储或提交失败返回 `ErrInboxUnavailable`。任何错误都返回空结果。回调只在 `err == nil` 时确认数据库受理。
- 服务启动前使用独立迁移角色应用 `0006_feishu_inbox.sql`。待路由容量由装配层传给 `NewFeishuInbox`。受理最多使用 2.5 秒，留出长连接回调处理余量。

## 项目权威接口和八栏（F1）

OmniRelay 使用本地 PostgreSQL 管理项目资料与业务权限。项目目录从当前租户、应用和账号的有效成员记录读取。名称及阶段筛选、分页和总数基于同一快照。已有项目 ID、任务和群关联继续可用。档案未补全时，兼容展示项目 ID 和默认阶段。

栏目路径使用 `/api/v1/projects/{projectID}/` 前缀。项目读取前后复核有效成员权限。资料额外校验本地来源读权或飞书分享与群权限。飞书资料暂时不可核验时，回传授权的本地记录并标明部分可用。

| 栏目 | 路径 | 权威边界 |
| --- | --- | --- |
| 概览 | `overview` | 本地项目档案、里程碑与正式任务 |
| 成员 | `members` | 本地启用账号和有效成员关系 |
| 群聊 | `chats` | 本地群关联与飞书核验的读取权 |
| 会议纪要 | `meetings` | 已授权的本地纪要与该项目群中已核验的飞书纪要 |
| 文档 | `docs` | 已授权的本地项目资料与该项目群中已核验的飞书分享资料 |
| 日报 | `reports`、`reports/briefings` | 本地成员提交、简报记录与 E1 投递回执 |
| 任务 | `tasks` | 本地正式任务、看板、里程碑关联和活动 |
| 飞书 Bot | `feishu` | 本地接入配置和简报回执，使用飞书校验群与 Bot 状态 |

`PUT profile` 要求 `project.settings`，支持 `name`、`status`、`project_type`、`background` 与 `expected_version`。`PUT reports/mine/{date}` 要求 `project.comment`，支持 `note` 与 `expected_version`，仅写发起人的日报。两个写入均要求会话 CSRF，首次版本为 0，冲突返回 409。日报列表默认仅展示本人记录，项目设置管理员可查看同项目有效成员的提交。

群关联和目标群按租户及应用区分。选择目标群会建立关联，换目标群保留原群的关联。接入设置要求 `project.settings`，写入携带原 `version`，首次使用 0。计划记录 IANA 时区、本地执行时间和启用状态。计划存储和手动发布是独立动作。

`POST feishu/check` 要求 `project.settings`，检查其它账号还要求 `account.read`。输入包含 `account_id`（缺省为自己）、`action`（`project.read`、`project.assign`、`project.settings`）和 `public_scope`（`project_only` 或 `submission_count`）。结果独立报告 Bot、群成员、账号、项目动作和公开范围。校验保持只读，账号和项目动作读取 OmniRelay，飞书探测采用读取接口。

当前业务日没有可核验提交时，简报生成返回来源暂不可用。历史简报保留原生成时间和有权查看的投递回执，旧汇总无法核验时隐藏计数并阻止重新发布。简报每项目每业务日生成一条记录，固定生成者、数据版本、可见提交数和当时目标。默认仅预览。启用 E1 且项目管理者明确配置 `public_submission_count` 后，手动发布可向当前目标群发送业务日期和经批准的提交数。E1 发出前复核当前账号、项目动作、日报来源版本、目标群及内容。计划版本或目标变更后旧简报不会改投。投递与生成独立，未知回执通过 `POST reports/briefings/{runID}/reconcile` 查询原操作。

## 群归属和项目主群（D1）

- `GET /api/v1/feishu/chats` 与 `GET /api/v1/feishu/chats/{chatID}` 的群聊条目返回 `project_assignment`，其中含 `project_id`、`primary` 和 `version`。未归属时项目 ID 是空字符串。私聊和归属项目无读权的群省略该字段。使用稳定项目 ID 匹配项目目录中的最新名称。
- `GET /api/v1/projects/{projectID}/chats` 返回同一份关系。列表含 `project_id`、`primary_version`、`items` 和 `total`。群条目含 `id`、`name`、`project_id`、`primary` 和 `version`。项目列表按当前群来源权限过滤后计数。消息、搜索和群成员沿用全局聊天接口。
- `PUT /api/v1/feishu/chats/{chatID}/project` 携带读取到的 `version` 和目标 `project_id`，空字符串表示清除。成功返回更新后的归属对象。主群离开原项目时同时清除其主群身份。若该群也是原项目的简报目标，同时撤销该目标并关闭该计划的启用和公开提交数选项。
- `PUT /api/v1/projects/{projectID}/chats/primary` 携带 `chat_id`、该群的 `assignment_version`（列表项的 `version`）和列表的 `primary_version`。从当前项目的群中选定主群，成功返回 `chat_id` 和新的 `primary_version`。替换时原主群继续留在项目里。切换主群后重新读取受影响的群版本。
- 写入要求会话通过 CSRF 校验、操作者具备所操作群的当前成员及来源读权，并持有相关项目的 `project.settings`。跨项目移动要同时具备原项目与目标项目的权限。失权返回 404，陈旧版本返回 409，客户端应重新读取。关系、版本和带前后状态的审计同一事务提交。

## 本地任务、里程碑和移动（G1）

任务、看板版本、里程碑和移动回执按租户、应用、项目隔离。以下路径相对于 `/api/v1/projects/{projectID}`。读取使用 `project.read`，任务与里程碑写入使用 `project.assign`。写操作沿用会话 CSRF 校验。

- `GET /tasks` 返回四列看板、项目版本和筛选后的总数。看板与列表共用列内的正式任务 ID；`GET /tasks/{taskID}` 返回同一任务。可组合传递 `q`、`owner_member_id` 和 `milestone_id`。省略里程碑条件表示全部，`milestone_unassigned=true` 表示未关联。分页使用列 ID 与响应中的游标。
- `POST /tasks` 创建，`PUT /tasks/{taskID}` 携带 `expected_task_version` 完整保存属性。`PATCH /tasks/{taskID}/milestone` 只需同一版本和 `milestone_id`，显式传入 null 可清除归属。`GET /milestones` 和 `POST /milestones` 读取、创建项目里程碑。多个任务可属于同一里程碑。节点可另以 `task_id` 引用任务，节点的负责人及状态由该任务驱动。
- P0/P1/P2/P3 依次显示紧急、重要、普通、低。任务期限可传自然日或带偏移的时间。读模型在项目业务时区派生日期显示和告警；未来三天、当天和逾期告警，已完成、已取消及无期限均排除。同年展示月日，跨年带年份。缺省时区为 Asia/Shanghai，`PUT /tasks/board/timezone` 使用 `project.settings`、`business_timezone` 和 `expected_board_version` 修改。
- `POST /tasks/{taskID}/move` 接收 `target_column_id`、`expected_task_version`、`expected_board_version` 和 `request_key`；手动排序可附 `before_id`、`after_id`。成功后返回含双版本的任务，同一操作者重试原命令可重放首次回执。HTTP 409 的 `task_version_conflict`、`board_version_conflict`、`move_request_key_conflict` 分别标识任务陈旧、看板陈旧、同键异参。

### 正式任务与本地授权（H1）

OmniRelay PostgreSQL 持有任务、项目成员、动作权限、标签、来源读权和活动历史。P08 仅作离线对照，固定 OmniGrid 提交 `9ae3636948b9a7e42d0ed2f6e0a265c81c65383b`。仓内保留该提交的 [详情、评论与活动切片](p08/openapi.json) 和 [看板切片](p08/openapi-board.json)。以下路径均相对 `/api/v1/projects/{projectID}`。

- `PUT /members/{accountID}/access` 接收 `role` 和可选的 `member_id`。系统角色管理员可为没有有效管理员的项目显式指派首位 `manager`。之后由项目管理员授权，最后一位有效管理员受保护。首次绑定可指定历史成员标识以承接既有任务。撤权保留标识绑定，清除该账户的来源读权。
- `reader` 可读任务。`member` 可评论，并可更新本人负责或参与的非终态任务进展、提交反馈。`editor` 可管理任务字段、标签、里程碑及审核反馈。`manager` 还可管理成员和来源读权。`revoked` 收回项目访问。`GET /tasks/{taskID}/actions` 返回当前账号对该任务的动作，写入事务再次核对授权。
- `GET /members` 与 `GET /task-tags` 提供本项目候选项。`PUT /task-tags/{tagID}` 接收名称 `name`。完整保存沿用 G1 的 `expected_task_version`，支持 `request_key`；移动同时校验看板版本。创建、保存、移动、调整里程碑及采纳反馈均写正式历史。旧任务创建者和创建时间缺失时返回空值。
- `PUT /sources/{kind}/{sourceID}` 登记本地来源及 `reader_account_ids`。文档和纪要接收 `title`、`body`，消息接收 `chat_id`，按当前群权限、项目归属及有效归档读取。`PUT /sources/{kind}/{sourceID}/readers` 单独替换读者清单。项目成员资格与来源读权分别校验。`PUT /tasks/{taskID}/evidence/{kind}/{sourceID}` 由有任务管理权和来源读权的操作者建立关联。`GET /tasks/{taskID}/evidence` 逐项复核，失权项仅返回通用提示。
- `POST /tasks/{taskID}/update-suggestions` 携带 `task_version`、`request_key`、`proposed_status` 和 `note`。`GET` 同路径读取可见建议。`POST /task-update-suggestions/{suggestionID}/review` 携带建议的 `expected_version`、`action` 和 `reason`。采纳时还核对建议所依据的任务版本，任务、审核结果及正式历史在同一事务提交。
- `POST /tasks/{taskID}/comments` 携带 `expected_version`、`request_key` 和 `body`。评论重试返回原回执，任务内容版本保持原值。`GET /tasks/{taskID}/activity` 支持 `kind=all|change|comment`、`limit` 和 `cursor`。客户端原样回传 `next_cursor`，服务端按时间和事件 ID 倒序读取。活动保存服务端账号、名称、时间及变化字段。
