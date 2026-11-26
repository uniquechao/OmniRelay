// Package agentadmin owns desired Agent configuration, version acknowledgements
// and the workbench's access-controlled view of execution status.
package agentadmin

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
)

type Service struct {
	DB    *pgxpool.Pool
	Roles *access.Service
}

type Config struct {
	Role           string   `json:"role"`
	Name           string   `json:"name"`
	Persona        string   `json:"persona"`
	Tools          []string `json:"tools"`
	Enabled        bool     `json:"enabled"`
	Version        int64    `json:"version"`
	AppliedVersion int64    `json:"applied_version"`
	FailedVersion  int64    `json:"failed_version"`
}

var names = map[string]string{"project-pm": "项目 PM", "query": "知识问答 Agent", "management-pm": "全局管理 Agent"}
var errConflict = errors.New("configuration version changed")

func Load(ctx context.Context, tx pgx.Tx, tenant, app, scope, role string) (Config, error) {
	name, ok := names[role]
	if !ok || (role == "project-pm") != (scope != "") {
		return Config{}, errors.New("invalid Agent scope")
	}
	c := Config{Role: role, Name: name, Enabled: true}
	err := tx.QueryRow(ctx, `SELECT name,persona,tools,enabled,version,applied_version,failed_version
        FROM agent_configurations WHERE (tenant_key,app_id,scope,role)=($1,$2,$3,$4)`, tenant, app, scope, role).Scan(&c.Name, &c.Persona, &c.Tools, &c.Enabled, &c.Version, &c.AppliedVersion, &c.FailedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	return c, err
}

func save(ctx context.Context, tx pgx.Tx, tenant, app, scope, actor, reason string, next Config) (Config, error) {
	prev, err := Load(ctx, tx, tenant, app, scope, next.Role)
	if err != nil {
		return Config{}, err
	}
	if next.Version != prev.Version {
		return Config{}, errConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent_configurations (tenant_key,app_id,scope,role,name,persona,tools,enabled,version)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1)
        ON CONFLICT (tenant_key,app_id,scope,role) DO UPDATE SET name=excluded.name,persona=excluded.persona,
        tools=excluded.tools,enabled=excluded.enabled,version=agent_configurations.version+1
        WHERE agent_configurations.version=$9`, tenant, app, scope, next.Role, next.Name, next.Persona, next.Tools, next.Enabled, next.Version)
	if err != nil {
		return Config{}, err
	}
	if tag.RowsAffected() != 1 {
		return Config{}, errConflict
	}
	curr, err := Load(ctx, tx, tenant, app, scope, next.Role)
	if err != nil {
		return Config{}, err
	}
	before, err := json.Marshal(prev)
	if err != nil {
		return Config{}, err
	}
	after, err := json.Marshal(curr)
	if err != nil {
		return Config{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_configuration_changes(tenant_key,app_id,scope,role,version,account_id,reason,previous,updated)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenant, app, scope, next.Role, curr.Version, actor, reason, before, after)
	return curr, err
}

func Applied(ctx context.Context, tx pgx.Tx, tenant, app, scope, role string, version int64) error {
	_, err := tx.Exec(ctx, `UPDATE agent_configurations SET applied_version=$5,failed_version=CASE WHEN failed_version<=$5 THEN 0 ELSE failed_version END
      WHERE (tenant_key,app_id,scope,role)=($1,$2,$3,$4) AND $5 BETWEEN applied_version AND version`, tenant, app, scope, role, version)
	return err
}
func ApplyFailed(ctx context.Context, tx pgx.Tx, tenant, app, scope, role string, version int64) error {
	_, err := tx.Exec(ctx, `UPDATE agent_configurations SET failed_version=$5 WHERE (tenant_key,app_id,scope,role)=($1,$2,$3,$4)
       AND version=$5 AND applied_version<$5`, tenant, app, scope, role, version)
	return err
}
