package repository

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zanel1u/cloud-cli-proxy/internal/store/migrations"
	"github.com/zanel1u/cloud-cli-proxy/internal/store/migrator"
	_ "modernc.org/sqlite"
)

// TestMigration0006_DisguiseCPUModel 验证：
// 1. 空库全量迁移（含 0006）成功
// 2. 迁移后 hosts.disguise_cpu_model 存在且默认空串（旧行回落镜像默认伪装）
// 3. UpdateHostDisguiseCPUModel 设置 → GetHost 读回往返一致
// 4. 空串清除（回落镜像默认 AMD EPYC 7763）
// 5. 不存在的 host 返回 sql.ErrNoRows（admin API 据此返回 404）
func TestMigration0006_DisguiseCPUModel(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// 步骤 1：全量迁移（含 0006_disguise_cpu_model.sql）
	if err := migrator.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("migrator.RunMigrations: %v", err)
	}

	// 造最小数据链：users → hosts
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, entry_password) VALUES ('u1', 'alice', 'pw')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO hosts (id, user_id, status, template_image_ref, home_volume_name, slot_key)
		 VALUES ('h1', 'u1', 'running', 'img:v1', 'vol-1', 'primary')`); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	repo := New(db)

	// 步骤 2：默认值 ''
	host, err := repo.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.DisguiseCPUModel != "" {
		t.Errorf("迁移后默认应为空串，实际 %q", host.DisguiseCPUModel)
	}

	// 步骤 3：设置 → 读回往返
	const wantModel = "Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz"
	if err := repo.UpdateHostDisguiseCPUModel(ctx, "h1", wantModel); err != nil {
		t.Fatalf("UpdateHostDisguiseCPUModel: %v", err)
	}
	host, err = repo.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost after update: %v", err)
	}
	if host.DisguiseCPUModel != wantModel {
		t.Errorf("roundtrip = %q, want %q", host.DisguiseCPUModel, wantModel)
	}

	// 列表查询也带出该列
	hosts, err := repo.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 1 || hosts[0].DisguiseCPUModel != wantModel {
		t.Errorf("ListHosts 应透传 disguise_cpu_model，实际 %+v", hosts)
	}

	// 步骤 4：空串清除
	if err := repo.UpdateHostDisguiseCPUModel(ctx, "h1", ""); err != nil {
		t.Fatalf("UpdateHostDisguiseCPUModel clear: %v", err)
	}
	host, err = repo.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost after clear: %v", err)
	}
	if host.DisguiseCPUModel != "" {
		t.Errorf("清除后应为空串（回落镜像默认 AMD EPYC），实际 %q", host.DisguiseCPUModel)
	}

	// 步骤 5：不存在 → sql.ErrNoRows
	if err := repo.UpdateHostDisguiseCPUModel(ctx, "missing", wantModel); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("不存在 host 应返回 sql.ErrNoRows，实际 %v", err)
	}
}
