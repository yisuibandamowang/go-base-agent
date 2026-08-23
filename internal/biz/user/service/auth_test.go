package service

import (
	"context"
	"strings"
	"testing"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	userModel "go-base-agent/internal/biz/user/model"
	userRepo "go-base-agent/internal/biz/user/repo"
	"go-base-agent/internal/framework/config"
	appctx "go-base-agent/internal/framework/context"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuthServiceChangePasswordRecordsSafeAudit(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	user := &userModel.User{Username: "alice", Password: string(oldHash), Role: "user", Avatar: "avatar.png"}
	if err := gdb.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	svc := NewAuthService(userRepo.NewUserRepo(gdb), config.AuthConfig{})
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID: user.ID, Username: user.Username, Role: user.Role,
	})
	if err := svc.ChangePassword(ctx, user.ID, "old-secret", "new-secret"); err != nil {
		t.Fatalf("change password: %v", err)
	}

	var log auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeUser, user.ID).First(&log).Error; err != nil {
		t.Fatalf("load audit log: %v", err)
	}
	if log.OperationType != auditService.OperationUpdate || log.OperatorID != user.ID || log.ActionDesc != "修改当前用户密码" {
		t.Fatalf("unexpected audit log: %+v", log)
	}
	joined := strings.Join([]string{log.BeforeSnapshot, log.AfterSnapshot, log.ChangeDiff}, "\n")
	if !strings.Contains(joined, `"username":"alice"`) || !strings.Contains(joined, `"role":"user"`) {
		t.Fatalf("expected non-sensitive user snapshot, got %s", joined)
	}
	for _, secret := range []string{"old-secret", "new-secret", string(oldHash), `"password"`} {
		if strings.Contains(joined, secret) {
			t.Fatalf("audit snapshot leaked password material %q: %s", secret, joined)
		}
	}
}
