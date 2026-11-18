# 飞书接入可执行 issue 计划

来源：`design/specs/omni-relay-feishu-integration.md` §1–6。目标：在现有 Go/Web 工程上新增 Bot 管理与可信入站，补齐 OAuth 与目录同步，并完成真实预发布验收。

工程师必读：[飞书接入工程师实现标准与约束](../../design/engineering/feishu-integration-standards.md)。其中分别初始化后端、前端、联调与验收职责、实现约束、测试证据及完成定义；每项 issue 正文均引用该文件。

## 现状与范围

- 已有：OAuth/PKCE、显式账号准入、会话与权限、目录分页/租约/快照、用户管理界面。任务 5/6/8/9 以补差和回归为主。
- 待建：Bot 配置/检测、SDK/Lease 连接、入站领域与数据库受理；当前相关目录多为空占位。任务 2/3/4/7 承接新增交付。
- `.panda/modules/INDEX.md` 和已有 issue 记录当前不存在，省略 module/agent 绑定，不创建模块。执行时若已初始化，先按项目入口读取对应模块与过程页。
- 当前配置强制提供应用凭据，新增“凭据缺失”诊断需在任务 2 中明确兼容处理；不能放松现有会话/HTTPS/数据库 TLS 要求。
- 当前前端 API 包装不支持自定义条件头或获取 ETag，任务 7 必须补齐。已有登录失败分支混用 JSON 与旧 feishu 回跳，任务 5/8 统一。
- 本计划不建设完整 Agent、业务路由/派单或消息发送；入站止于持久化待路由工作。真实平台能力须在任务 10 验证，不能由样例推定。

## 执行顺序与职责

| 编号 | 负责工程师 | 交付 | 前置 |
| --- | --- | --- | --- |
| 1 | 联调与验收 | 契约样例、跨角色矩阵和环境清单 | 无 |
| 2 | 后端 | Bot 配置/检测/权限审计 | 1 |
| 3 | 后端 | 事务入站、去重与代次保护 | 2 |
| 4 | 后端 | SDK、Lease 与消息过滤 | 3 |
| 5 | 后端 | OAuth 错误回跳和准入回归 | 1 |
| 6 | 后端 | 目录范围与快照恢复 | 1 |
| 7 | 前端 | Bot 配置页和条件请求 | 1 |
| 8 | 前端 | 登录提示及返回页面 | 1 |
| 9 | 前端 | 目录与同步状态 | 1 |
| 10 | 联调与验收 | 真实预发布联合验收 | 4、5、6、7、8、9 |

全部 10 项任务指定 `agent: codex` 执行；后端、前端、联调验收仍按各自工程师标准约束。依赖表达交付契约或执行前置。任务 1 后后端 2/5/6 和前端 7/8/9 可依样例独立推进；前端任务完成前需分别与后端 2/5/6 联调，这是交付检查，不增加阻止样例开发的前置。任务 2 → 3 → 4 是 Bot 严格依赖链。每项自带实现、测试和文档，不把单测留到任务 10。

真实验收顺序固定为：应用及凭据准备 → 配置检测 → OAuth → 通讯录 → 私聊/群 @；应用后台操作、测试消息及换钥在独立预发布授权范围内实施。缺外部资源时先交付可验证代码与实验步骤，真实验收明确留待完成。

## 需求覆盖与重点检查

- 设计 §1–2：任务 1/2 和全员通用约束；角色撤销、CSRF、秘密采集由各实现任务回归，10 联合核验。
- 设计 §3：任务 2/3/4/7；尤其检查 PATCH 与停用时晚到回调、旧任务换代提交、重复消息和未收信状态。
- 设计 §4：任务 5/8；尤其检查整页跳转导致原路由丢失、并发回调、未知/外部返回目标。
- 设计 §5：任务 6/9；尤其检查快照切换中多页结果混合、撤权后旧浏览器请求回写、直接授权个人和 unknown 在职字段。
- 设计 §6：任务 10；用真实平台核验 SDK、权限、回调登记、三链路隔离与身份一致。

以下是供 PandaDOS 预览和导入的唯一计划块；编号为数组顺序，不是正式 issue key。生成文件不创建或排队执行 issues。

```pandados-plan
{
  "schemaVersion": 1,
  "planId": "feishu-integration-v1",
  "title": "飞书 Bot 配置、网页登录与组织人员同步接入",
  "issues": [
    {
      "title": "固定飞书接入契约、接口样例与跨角色验收矩阵",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、联调与验收工程师和完成定义。\n负责角色：联调与验收工程师。\n范围：设计 §2–5；tests/contract、tests/fixtures；config/backend.env.example；design/engineering。\n实施：为 Bot GET/PATCH/checks、OAuth 错误回跳、目录快照整理可复用样例和可执行契约检查；补全未明确的检测任务响应字段、幂等键保留规则、心跳过期阈值和凭据代次配置名并标明实施约定，保持设计规定值。准备预发布资源清单及 SDK 验证步骤，不以外部环境就绪作为开发的硬阻塞。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "样例覆盖 Bot 配置初始 0/关闭、ETag、所有连接和检测枚举、UTC RFC 3339、last_message_at=null、四种脱敏检测错误。",
        "HTTP 矩阵覆盖 200/202/400/401/403/404/409/412/428，以及 Origin、CSRF、If-Match、Idempotency-Key 的正反例；明确检测键与活动任务合并及凭据换代的关系。",
        "提供 cancelled/denied/unavailable/旧 feishu 回跳和目录 snapshot_version、coalesced、可见性/失败状态样例，后续前后端可直接复用。",
        "自动校验样例结构与枚举；列出真实应用所需权限、可用范围、测试账号/群、HTTPS 回调与 SDK 待验证项，不包含凭据。"
      ],
      "dependsOn": [],
      "agent": "codex"
    },
    {
      "title": "实现 Bot 配置、异步连接检测和权限审计接口",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、后端工程师和完成定义。\n负责角色：后端工程师。\n范围：设计 §2、§3 管理接口；internal/system、internal/access/access.go、internal/store/postgres、migrations、cmd/omnirelay/config.go 与 server.go。\n实施：实现配置存储与四个管理接口、检测 Worker 和真实凭据/Bot 信息读取适配；扩展 /me 的 access.admin.feishu_bot。部署注入非敏感凭据代次。管理 GET 读数据库，检测不操作连接或发送消息。SDK 类型留在适配器。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "GET /api/v1/system/feishu-bot 返回脱敏状态和 ETag；默认 version=0/enabled=false，缺凭据明确标识；反复 GET 不增加飞书调用。",
        "PATCH enabled 使用 If-Match：缺版本428、过期412、未知字段400；有效变更与审计同事务，相同值不增长版本或产生变更审计。",
        "未登录401、普通成员403；运维能读取和检测但不能改开关；管理员可修改；POST/PATCH 同源与 CSRF 测试通过，角色撤销下一次请求生效。",
        "POST checks 接收空 JSON/Idempotency-Key 返回202；同键返回原任务，同应用同凭据代次的活动检测合并；GET 单次状态越界/不存在404，queued/running/succeeded/failed 转移可查询。",
        "测试真实适配的 tenant_token/bot_info passed/failed/skipped、超时/拒绝/缺凭据错误映射；任务异常退出可回收，旧领取版本/凭据代次不能更新当前状态。",
        "数据库升级及并发任务唯一性测试实际运行；日志/响应/审计不含凭据，检测成功不伪造 connected 或 last_message_at。"
      ],
      "dependsOn": [
        1
      ],
      "agent": "codex"
    },
    {
      "title": "实现可去重且受连接代次保护的持久入站受理",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、后端工程师和完成定义。\n负责角色：后端工程师。\n范围：设计 §3 收消息及 Go 服务设计 §2.1；internal/orchestrator、internal/store/postgres、internal/identity、migrations、tests/contract。\n实施：落地 InboxAcceptor、InboundEvent、AcceptResult 与最小待路由工作存储。建立组织/应用/配置版本/连接代次校验及原子受理。复用已启用账号解析，不创建账号；本项不实现路由、DSH 或发送。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "按设计定义 AppID/TenantID/EventID/MessageID、会话/话题/回复对象、发送人、时间和正文；SDK 报文类型不进入领域契约。",
        "(app_id, tenant_id, message_id) 数据库唯一；并发重复返回同一 work_id，事件与待路由工作同事务，容量检查在去重之后。",
        "配置关闭、版本/连接代次过期或持有权失效的晚到事件被事务拒绝；提交失败不更新接收时间、不确认成功。",
        "Bot 已验证身份与 Web 复用已启用账号；未开通/停用/异组织不自动建档或取得可执行身份；原始受理与准入拒绝的记录边界有明确测试。",
        "新连接代次 last_message_at 清空；仅本代次合规真实事件提交后更新；真实 PostgreSQL 验证重复、回滚、停用与换代竞态。"
      ],
      "dependsOn": [
        2
      ],
      "agent": "codex"
    },
    {
      "title": "接入飞书 SDK 长连接、Kubernetes Lease 与可信消息过滤",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、后端工程师和完成定义。\n负责角色：后端工程师。\n范围：设计 §3 Go 到飞书；internal/feishu、cmd/omnirelay、config/backend.env.example、deploy/k8s 的 Bot Lease/RBAC 最小资源。\n实施：固定官方 Go SDK 版本，装配可取消连接生命周期、退避重连与有效心跳；持有者协调最新配置，PostgreSQL 代次保护最终写入。将可信私聊/群 @ 映射到 issue 3 受理接口，完整标注需要真实 SDK 实测的行为。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "双实例只由 Lease 持有者开启连接；失去 Lease、开关关闭或凭据换代会停收并拒绝旧代次提交；滚动交接、重启及取消退出没有遗留连接或 goroutine。",
        "持久记录 disabled/connecting/connected/reconnecting/unavailable 及配置版本/代次；过期心跳返回 unavailable，旧实例不能覆盖新代次在线状态。",
        "仅受理 im.message.receive_v1 中来源应用/组织匹配的人类私聊及明确 @ 当前 Bot open_id 的群消息；过滤其他 Bot、无关 @ 和错误来源；保留原话题与回复消息 ID。",
        "回调只校验与持久化，提交后确认；自动化覆盖重复消息、数据库失败和晚到回调，记录 3 秒限制内的处理证据。",
        "SDK 启停、在线信号、处理超时/重投及断线重连有版本化验证步骤；最小 Lease/RBAC 配置可复现，真实群和私聊最终由 issue 10 验收，不能用 mock 声称真机通过。"
      ],
      "dependsOn": [
        3
      ],
      "agent": "codex"
    },
    {
      "title": "补齐 OAuth 固定错误回跳与账号准入回归",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、后端工程师和完成定义。\n负责角色：后端工程师。\n范围：设计 §4；internal/identity/identity.go、feishu.go、identity_test.go；cmd/omnirelay/backend_test.go。\n实施：在现有 state/PKCE/会话基础上改进失败回跳，区分用户取消、准入拒绝及上游不可用。复用现有明确开通账号策略，返回路径限制与新增 Bot 页面同步，补充脱敏观测。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "无效、过期、浏览器不匹配或重复 state 一律400且无新会话；只有通过事务验证的失败按 cancelled/denied/unavailable 303 到固定登录错误页。",
        "成功换码和身份读取后仅已开通且启用同租户同应用账号获得安全会话 Cookie；未开通、停用、异组织被拒绝。",
        "PKCE S256、浏览器绑定、原子消费、旧会话替换、退出204及停用/撤权回归测试通过；并发回调最多建立一次会话。",
        "允许站内合法返回路径含 Bot 配置页，拒绝外部地址和恶意返回目标；回跳目标仍重新鉴权。",
        "测试确认 callback 查询串、code/token/Cookie 不被日志或 APM 采集；可记录脱敏请求 ID 和错误码，原密码登录/改密行为不受影响。"
      ],
      "dependsOn": [
        1
      ],
      "agent": "codex"
    },
    {
      "title": "补齐目录授权范围、分页及快照恢复验证",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、后端工程师和完成定义。\n负责角色：后端工程师。\n范围：设计 §5；internal/directory/feishu.go、service.go、http.go 及现有目录/账号测试。\n实施：优先复用现有分页、租约和快照代码，按设计补差并固定真实 API 的身份字段与权限验证步骤。覆盖直接授权人员、多部门、移动、未知在职状态及范围变化；保持目录与账号独立。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "读取 scopes、部门/人员完整分页和直接授权人员，按 Open ID 去重并保留有权查看的全部部门路径；多部门、同名、调岗、unknown 在职状态有自动测试。",
        "发布前重读并比对授权范围；重复 page_token、缺页、非法父子/人员归属、超时及限流耗尽不得发布不完整快照。",
        "同范围临时失败保留可见旧快照；确认收窄/撤权先隐藏旧目录，拒绝旧 snapshot_version 后续读取，完整验证后原子切换。",
        "部门/人员分页共享 snapshot_version，变化返回409 directory_snapshot_changed；筛选、分页、根节点/直属子部门语义与设计一致。",
        "启动/周期/手动触发合并同源活动任务，重复手动请求仍审计；重启恢复、旧领取者提交失败及最近成功/下次时间/变更数有真实数据库测试。",
        "同步不自动创建账号/授予角色/停用会话；目录缺失、越界、已核实离职区别表达；真实范围与字段兼容性留存待 issue 10 核验证据。"
      ],
      "dependsOn": [
        1
      ],
      "agent": "codex"
    },
    {
      "title": "实现飞书 Bot 管理页面及条件请求支持",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、前端工程师和完成定义。\n负责角色：前端工程师。\n范围：设计 §3 工作台；web/index.html、app.js、system-api.js、login.js，新增 web/feishu-bot.js。\n实施：新增 #/system/feishu-bot 页面，复用既有原型组件、会话和反馈。扩展 API 包装以传递条件头/幂等键并读取 ETag，保持旧调用兼容。先用 issue 1 样例实现，再联调 issue 2。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "侧栏、移动选择器、直达路由和登录返回白名单接入；access.admin.feishu_bot 控制入口；管理员能保存，运维可看可测，普通成员无权直达。",
        "展示只读应用/组织、凭据注入标志、Bot 名称、OAuth 回调和启停草稿；不提供凭据编辑，组织同步入口按权限展示。",
        "GET ETag 用于 PATCH If-Match；保存与检测独立。412 刷新已保存版本但保留待编辑内容，用户再次确认保存；离开页面触发现有未保存提示。",
        "检测用 Idempotency-Key，网络重试沿用同键；只在 queued/running 轮询检测任务。凭据有效、Bot 可读、在线、真实收信分别展示；无消息提供测试群 @/私聊提示。",
        "处理未配置、428/412/401/403、平台不可用、任务失败和空时间；切页/退出停止轮询，晚到响应不覆盖新页面或草稿。",
        "真实接口验证条件头/CSRF；提供桌面/窄屏、浅深色、键盘与未保存流程证据；复用项目视觉技能，新页面缺少独立确认稿时注明验收边界。"
      ],
      "dependsOn": [
        1
      ],
      "agent": "codex"
    },
    {
      "title": "补齐前端飞书登录错误提示和原页面恢复",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、前端工程师和完成定义。\n负责角色：前端工程师。\n范围：设计 §4；web/login.js、web/app.js。\n实施：支持固定错误枚举并保持 error=feishu 兼容，修复整页 OAuth 跳转及取消重试后的站内目标恢复；先使用契约样例，最后与 issue 5 对接。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "cancelled、denied、unavailable 和旧 feishu 均显示可操作的本地化提示；取消后按钮恢复可重新登录，未知错误不显示原始提供方内容。",
        "成功回调后通过 /api/v1/me 确认会话和 CSRF，恢复原先有权页面（含 Bot 页）；取消/失败再登录仍保留合法目标，浏览器整页导航后可恢复。",
        "原目标被撤权时回工作台或显示无权状态，外部目标/未知路由不可恢复；401 进入登录、退出后旧会话不可继续使用。",
        "浏览器覆盖成功/取消/失败/重试、后退缓存页面恢复及权限变化；密码登录和改密回归通过，原型主题/减少动态效果保持一致。"
      ],
      "dependsOn": [
        1
      ],
      "agent": "codex"
    },
    {
      "title": "补齐组织与人员同步的可见性及失败恢复 UI",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、前端工程师和完成定义。\n负责角色：前端工程师。\n范围：设计 §5；web/admin.js、web/system-api.js。\n实施：在现有用户管理组织树、列表和同步详情上补差。保持同一快照分页，区分首次无快照、旧快照可见、同步失败和授权变化，修正 next sync/范围/变更数等未显示字段。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "组织筛选、姓名/账号状态查询及分页在同 snapshot_version 上；409 自动重新加载，尽量保留有效部门/筛选，不再可见的部门回到根。",
        "同步 queued/running、最近成功、下次同步、授权范围、人数/部门/变更数、失败原因可见；合并任务不显示为新建成功。",
        "失败但仍获授权的旧快照可浏览；确认撤权后及时清空旧列表/树/人员详情，晚到旧请求不得恢复已隐藏数据。",
        "同名人员显示组织路径，多部门关系不重复人员；“尚未开通”与目录在职状态分别展示，新增人员必须显式调用现有 accounts 接口开通。",
        "目录查看/同步权限分别控制；离开页面/会话失效停止轮询；与 issue 6 真实接口联调，浏览器覆盖窄屏/浅深色/键盘焦点及失败恢复。"
      ],
      "dependsOn": [
        1
      ],
      "agent": "codex"
    },
    {
      "title": "完成预发布真实飞书三链路联调与故障验收",
      "category": "task",
      "body": "依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、联调与验收工程师和完成定义。\n负责角色：联调与验收工程师。\n范围：设计 §6 交付顺序；config、deploy/k8s 的接入说明、tests/integration、design/engineering 下验收记录。\n实施：准备独立预发布自建应用/凭据、能力/可用范围/权限、精确 HTTPS 回调、消息订阅和测试成员/群；依次验证配置检测、OAuth、目录、Bot 入站。记录真实 SDK 版本与平台行为，补充必要修复及回归证据。\n交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。",
      "acceptance": [
        "配置读取/检测通过，鉴别凭据有效但未连接/未收信；真实应用已建立首个连接后保存长连接配置并发布订阅，权限与范围有脱敏检查记录。",
        "真机 OAuth 覆盖成功回原页、取消重试、未开通/停用/异组织拒绝、回调重放及退出失效；Web/Bot 同一成员解析到同一 account_id。",
        "真实目录覆盖限定部门、直接授权个人、多部门/调岗、范围收窄/撤回；分页失败仍能查看获授权旧快照，撤回后浏览器和接口旧数据不可读。",
        "真实用户私聊和群 @ 本 Bot 提交入站与待路由工作；无关消息被过滤，重复消息只一份事件/工作，记录本代次接收时间及回调处理时限。",
        "双实例/Lease 丢失/滚动更新/进程重启/掉线重连验证所有权与拒绝旧代次提交；关闭 Bot 后 OAuth 和目录继续可用。",
        "预发布凭据轮换分别复测换码、Bot 重连、目录读取；旧检测和旧在线状态不污染当前代次，日志/审计/截图/APM 无秘密。",
        "提交真实数据库测试、浏览器验证与飞书请求 ID 等脱敏证据；无凭据或授权不足时明确待验证，最终任务保持未完成，不能用 mock 结果替代。"
      ],
      "dependsOn": [
        4,
        5,
        6,
        7,
        8,
        9
      ],
      "agent": "codex"
    }
  ]
}
```
