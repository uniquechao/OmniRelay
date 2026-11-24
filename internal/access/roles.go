package access

type roleDefinition struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Scope       string   `json:"scope"`
	Default     bool     `json:"default"`
	Grantable   bool     `json:"grantable"`
	Permissions []string `json:"permissions"`
	actions     []string
}

var systemActions = []string{
	"account.read", "account.manage", "role.read", "role.manage", "directory.read",
	"directory.sync", "system.read", "system.configure", "system.operate", "audit.read", "chat.read_all", "project.create", "dsh.debug",
}

var actionDescriptions = map[string]string{
	"account.read": "查看系统账号", "account.manage": "开通和启停系统账号",
	"role.read": "查看角色和授权记录", "role.manage": "授予和撤销系统角色",
	"directory.read": "查看飞书通讯录和同步记录", "directory.sync": "同步飞书通讯录",
	"system.read": "查看连接设置、飞书 Bot 配置和检测结果", "system.configure": "修改飞书 Bot 配置和启停状态",
	"system.operate": "执行飞书 Bot 连接检测", "audit.read": "查看系统审计日志",
	"chat.read_all":  "读取此部署已授权的飞书 Bot 会话",
	"project.create": "新建项目",
	"dsh.debug":      "进入 DSH 原生调试界面",
}

var roleCatalog = map[string]roleDefinition{
	RoleSystemUser: {
		ID: RoleSystemUser, Name: "普通用户", Scope: "system", Default: true,
		Permissions: []string{"登录系统", "维护本人资料"},
	},
	RoleSystemAdmin: {
		ID: RoleSystemAdmin, Name: "系统管理员", Scope: "system", Grantable: true,
		actions: systemActions,
	},
	RoleSystemOperator: {
		ID: RoleSystemOperator, Name: "系统运维者", Scope: "system", Grantable: true,
		actions: []string{"account.read", "role.read", "directory.read", "directory.sync", "system.read", "system.operate", "audit.read"},
	},
	RoleAuditViewer: {
		ID: RoleAuditViewer, Name: "审计查看者", Scope: "system", Grantable: true,
		actions: []string{"audit.read"},
	},
	RoleProjectAdmin: {
		ID: RoleProjectAdmin, Name: "项目管理员", Scope: "project", Grantable: true,
		Permissions: []string{"在有效项目成员关系和授权期间维护项目设置"},
	},
	RoleProjectMember: {
		ID: RoleProjectMember, Name: "项目成员", Scope: "project",
		Permissions: []string{"访问所属项目", "按正式项目权限执行派单等操作"},
	},
}

func listRoleDefinitions() []roleDefinition {
	order := []string{RoleSystemUser, RoleSystemAdmin, RoleSystemOperator, RoleAuditViewer, RoleProjectAdmin, RoleProjectMember}
	items := make([]roleDefinition, 0, len(order))
	for _, id := range order {
		role := roleCatalog[id]
		for _, action := range role.actions {
			role.Permissions = append(role.Permissions, actionDescriptions[action])
		}
		items = append(items, role)
	}
	return items
}
