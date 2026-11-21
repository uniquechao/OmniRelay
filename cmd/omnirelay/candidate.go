package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/migrations"
)

// 候选实例有独立 PostgreSQL 集群。控制标记由初始化身份写入，运行和迁移角色只读。
func checkCandidateDatabase(ctx context.Context, pool *pgxpool.Pool, command string) error {
	expected := os.Getenv("OMNIRELAY_CANDIDATE_ID")
	var matches bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM candidate_guard.identity g JOIN pg_roles r ON r.rolname=current_user
 WHERE g.id=$1 AND current_database()=g.db_name AND current_user=CASE WHEN $2='migrate' THEN g.migration_role ELSE g.runtime_role END
 AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
 AND NOT has_schema_privilege(current_user,'candidate_guard','CREATE')
 AND NOT has_table_privilege(current_user,'candidate_guard.identity','INSERT,UPDATE,DELETE,TRUNCATE'))`, expected, command).Scan(&matches)
	if expected == "" || err != nil || !matches {
		return errors.New("候选数据库、角色或隔离标记校验失败")
	}
	return nil
}

func candidateHandler(pool *pgxpool.Pool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if checkCandidateDatabase(ctx, pool, "serve") != nil || migrations.Check(ctx, pool) != nil {
			http.Error(w, "候选库未就绪", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return secureHeaders(mux)
}
