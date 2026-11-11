package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"

	"gorm.io/gorm"
)

type userLogTestServices struct {
	database *gorm.DB
	auth     *AuthService
	users    *UserService
	roles    *RoleService
}

func newUserLogTestServices(t *testing.T, name string) userLogTestServices {
	t.Helper()
	database := newAuthTestDB(t, name)
	logs := NewUserLogService(repository.NewUserLogRepository(database), "node-test")
	return userLogTestServices{
		database: database,
		auth:     newTestAuthService(database).WithUserLogs(logs),
		users: NewUserService(
			repository.NewUserRepository(database),
			repository.NewRoleRepository(database),
			repository.NewUserTokenRepository(database),
		).WithUserLogs(logs),
		roles: NewRoleService(repository.NewRoleRepository(database)).WithUserLogs(logs),
	}
}

func userLogRows(t *testing.T, database *gorm.DB) []model.UserLog {
	t.Helper()
	var rows []model.UserLog
	if err := database.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("Find(user_logs) error = %v", err)
	}
	return rows
}

func lastUserLog(t *testing.T, database *gorm.DB) model.UserLog {
	t.Helper()
	rows := userLogRows(t, database)
	if len(rows) == 0 {
		t.Fatal("no user log rows")
	}
	return rows[len(rows)-1]
}

func uintValue(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func assertUserLog(t *testing.T, row model.UserLog, action model.UserLogAction, success bool, userID, actorID uint, messagePart string) {
	t.Helper()
	if row.Action != action || row.Success != success {
		t.Fatalf("row action=%s success=%t, want %s %t (row=%+v)", row.Action, row.Success, action, success, row)
	}
	if uintValue(row.UserID) != userID {
		t.Fatalf("row user_id=%d, want %d", uintValue(row.UserID), userID)
	}
	if uintValue(row.ActionUserID) != actorID {
		t.Fatalf("row action_user_id=%d, want %d", uintValue(row.ActionUserID), actorID)
	}
	if !strings.Contains(stringValue(row.Message), messagePart) {
		t.Fatalf("row message=%q, want it to contain %q", stringValue(row.Message), messagePart)
	}
	if row.NodeID != "node-test" {
		t.Fatalf("row node_id=%q, want node-test", row.NodeID)
	}
	if row.Timestamp.IsZero() {
		t.Fatal("row timestamp is zero")
	}
}

func TestUserLogLogin(t *testing.T) {
	svc := newUserLogTestServices(t, "user-log-login.db")
	user := createAuthTestUser(t, svc.database, "jsmith", model.UserStatusActive, "secret")
	createAuthTestUser(t, svc.database, "gone", model.UserStatusDeleted, "secret")

	if _, err := svc.auth.Login("jsmith", "secret", AuditContext{SourceAddress: "203.0.113.7"}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	row := lastUserLog(t, svc.database)
	assertUserLog(t, row, model.UserLogActionLogin, true, user.ID, user.ID, "")
	if stringValue(row.SourceAddress) != "203.0.113.7" {
		t.Fatalf("source_address=%q, want 203.0.113.7", stringValue(row.SourceAddress))
	}

	if _, err := svc.auth.Login("jsmith", "wrong", AuditContext{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(bad password) error = %v", err)
	}
	row = lastUserLog(t, svc.database)
	assertUserLog(t, row, model.UserLogActionLogin, false, user.ID, 0, "failed login for username jsmith: bad password")
	if row.SourceAddress != nil {
		t.Fatalf("source_address=%q, want nil", *row.SourceAddress)
	}

	if _, err := svc.auth.Login("nobody", "secret", AuditContext{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(unknown) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionLogin, false, 0, 0, "failed login for username nobody: unknown user")

	if _, err := svc.auth.Login("gone", "secret", AuditContext{}); !errors.Is(err, ErrInactiveUser) {
		t.Fatalf("Login(inactive) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionLogin, false, user.ID+1, 0, "inactive user")
}

func TestUserLogRefreshAndLogout(t *testing.T) {
	svc := newUserLogTestServices(t, "user-log-refresh.db")
	user := createAuthTestUser(t, svc.database, "jsmith", model.UserStatusActive, "secret")
	pair, err := svc.auth.Login("jsmith", "secret", AuditContext{})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	refreshed, err := svc.auth.Refresh(pair.RefreshToken, AuditContext{})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionTokenRefresh, true, user.ID, user.ID, "")

	if _, err := svc.auth.Refresh(pair.RefreshToken, AuditContext{}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Refresh(used token) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionTokenRefresh, false, 0, 0, "invalid token")

	expired := "expired-refresh-token"
	if err := svc.database.Create(&model.UserToken{
		UserID:            user.ID,
		RefreshHash:       security.HashOpaqueToken(expired),
		RefreshExpiration: time.Now().UTC().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("Create(user_token) error = %v", err)
	}
	if _, err := svc.auth.Refresh(expired, AuditContext{}); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("Refresh(expired) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionTokenExpired, false, user.ID, 0, "")

	if err := svc.auth.Logout(refreshed.AccessToken, AuditContext{}); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionLogout, true, user.ID, user.ID, "")

	if err := svc.auth.Logout("not-a-token", AuditContext{}); err == nil {
		t.Fatal("Logout(invalid) error = nil")
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionLogout, false, 0, 0, "")
}

func TestUserLogUserLifecycle(t *testing.T) {
	svc := newUserLogTestServices(t, "user-log-users.db")
	admin := createAuthTestUser(t, svc.database, "admin", model.UserStatusActive, "secret")
	audit := AuditContext{ActorUserID: &admin.ID, SourceAddress: "198.51.100.1"}
	editors, err := svc.roles.Create("editors", "", nil, audit)
	if err != nil {
		t.Fatalf("Create(role editors) error = %v", err)
	}
	viewers, err := svc.roles.Create("viewers", "", nil, audit)
	if err != nil {
		t.Fatalf("Create(role viewers) error = %v", err)
	}
	before := len(userLogRows(t, svc.database))

	created, err := svc.users.Create("jsmith", "John", "secret", []uint{editors.ID}, audit)
	if err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	rows := userLogRows(t, svc.database)[before:]
	if len(rows) != 2 {
		t.Fatalf("create rows = %d, want 2: %+v", len(rows), rows)
	}
	assertUserLog(t, rows[0], model.UserLogActionCreate, true, created.ID, admin.ID, "username=jsmith")
	assertUserLog(t, rows[1], model.UserLogActionUpdate, true, created.ID, admin.ID, "role added: editors")
	if uintValue(rows[1].RoleID) != editors.ID {
		t.Fatalf("role row role_id=%d, want %d", uintValue(rows[1].RoleID), editors.ID)
	}

	// Log in so the password change has a token to revoke.
	if _, err := svc.auth.Login("jsmith", "secret", AuditContext{}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	before = len(userLogRows(t, svc.database))

	name := "John Smith"
	password := "new-secret"
	roleIDs := []uint{viewers.ID}
	if _, err := svc.users.Update(created.ID, UpdateUserInput{Name: &name, Password: &password, RoleIDs: &roleIDs}, audit); err != nil {
		t.Fatalf("Update(user) error = %v", err)
	}
	rows = userLogRows(t, svc.database)[before:]
	if len(rows) != 4 {
		t.Fatalf("update rows = %d, want 4: %+v", len(rows), rows)
	}
	assertUserLog(t, rows[0], model.UserLogActionUpdate, true, created.ID, admin.ID, "name: John -> John Smith; password changed")
	if strings.Contains(stringValue(rows[0].Message), "new-secret") {
		t.Fatalf("update message contains password: %q", stringValue(rows[0].Message))
	}
	assertUserLog(t, rows[1], model.UserLogActionTokenRevoke, true, created.ID, admin.ID, "revoked 1 refresh tokens")
	assertUserLog(t, rows[2], model.UserLogActionUpdate, true, created.ID, admin.ID, "role added: viewers")
	assertUserLog(t, rows[3], model.UserLogActionUpdate, true, created.ID, admin.ID, "role removed: editors")

	before = len(userLogRows(t, svc.database))
	if _, err := svc.users.Delete(created.ID, audit); err != nil {
		t.Fatalf("Delete(user) error = %v", err)
	}
	rows = userLogRows(t, svc.database)[before:]
	if len(rows) != 1 {
		t.Fatalf("delete rows = %d, want 1: %+v", len(rows), rows)
	}
	assertUserLog(t, rows[0], model.UserLogActionDelete, true, created.ID, admin.ID, "status: active -> deleted")
	if stringValue(rows[0].SourceAddress) != "198.51.100.1" {
		t.Fatalf("source_address=%q, want 198.51.100.1", stringValue(rows[0].SourceAddress))
	}
}

func TestUserLogUserFailureRollsBack(t *testing.T) {
	svc := newUserLogTestServices(t, "user-log-user-failure.db")
	admin := createAuthTestUser(t, svc.database, "admin", model.UserStatusActive, "secret")
	audit := AuditContext{ActorUserID: &admin.ID}

	if _, err := svc.users.Create("jsmith", "", "secret", []uint{999}, audit); !errors.Is(err, ErrInvalidRoles) {
		t.Fatalf("Create(invalid roles) error = %v, want %v", err, ErrInvalidRoles)
	}
	var count int64
	if err := svc.database.Model(&model.User{}).Where("username = ?", "jsmith").Count(&count).Error; err != nil {
		t.Fatalf("Count(users) error = %v", err)
	}
	if count != 0 {
		t.Fatalf("user created despite failure, count=%d", count)
	}
	rows := userLogRows(t, svc.database)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want only the failure row: %+v", len(rows), rows)
	}
	assertUserLog(t, rows[0], model.UserLogActionCreate, false, 0, admin.ID, "invalid roles")

	if _, err := svc.users.Update(999, UpdateUserInput{}, audit); !svc.users.IsNotFound(err) {
		t.Fatalf("Update(missing) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionUpdate, false, 999, admin.ID, "update user 999 failed")

	if err := svc.users.LogPermissionDenied(model.UserLogActionDelete, &admin.ID, audit); err != nil {
		t.Fatalf("LogPermissionDenied() error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionDelete, false, admin.ID, admin.ID, "permission denied")
}

func TestUserLogWriteFailureFailsAction(t *testing.T) {
	svc := newUserLogTestServices(t, "user-log-write-failure.db")
	createAuthTestUser(t, svc.database, "jsmith", model.UserStatusActive, "secret")
	if err := svc.database.Migrator().DropTable(&model.UserLog{}); err != nil {
		t.Fatalf("DropTable(user_logs) error = %v", err)
	}

	if _, err := svc.auth.Login("jsmith", "secret", AuditContext{}); err == nil {
		t.Fatal("Login() error = nil, want log write error")
	}
	var tokens int64
	if err := svc.database.Model(&model.UserToken{}).Count(&tokens).Error; err != nil {
		t.Fatalf("Count(user_tokens) error = %v", err)
	}
	if tokens != 0 {
		t.Fatalf("user_tokens = %d, want 0 after rolled back login", tokens)
	}

	if _, err := svc.auth.Login("jsmith", "wrong", AuditContext{}); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(bad password) error = %v, want log write error", err)
	}

	if _, err := svc.users.Create("other", "", "secret", nil, AuditContext{}); err == nil {
		t.Fatal("Create(user) error = nil, want log write error")
	}
	var users int64
	if err := svc.database.Model(&model.User{}).Where("username = ?", "other").Count(&users).Error; err != nil {
		t.Fatalf("Count(users) error = %v", err)
	}
	if users != 0 {
		t.Fatalf("user created despite log write failure")
	}
}

func TestUserLogRoleLifecycle(t *testing.T) {
	svc := newUserLogTestServices(t, "user-log-roles.db")
	admin := createAuthTestUser(t, svc.database, "admin", model.UserStatusActive, "secret")
	audit := AuditContext{ActorUserID: &admin.ID}

	role, err := svc.roles.Create("editors", "", []RolePermissionInput{
		{Resource: string(model.PermissionResourceUsers), Action: string(model.PermissionActionRead)},
	}, audit)
	if err != nil {
		t.Fatalf("Create(role) error = %v", err)
	}
	row := lastUserLog(t, svc.database)
	assertUserLog(t, row, model.UserLogActionCreate, true, 0, admin.ID, "permissions=[users:read]")
	if uintValue(row.RoleID) != role.ID {
		t.Fatalf("role_id=%d, want %d", uintValue(row.RoleID), role.ID)
	}

	permissions := []RolePermissionInput{
		{Resource: string(model.PermissionResourceShares), Action: string(model.PermissionActionCreate)},
	}
	if _, err := svc.roles.Update(role.ID, UpdateRoleInput{Permissions: &permissions}, audit); err != nil {
		t.Fatalf("Update(role) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionUpdate, true, 0, admin.ID, "permissions added: shares:create; permissions removed: users:read")

	before := len(userLogRows(t, svc.database))
	if _, err := svc.roles.Update(role.ID, UpdateRoleInput{}, audit); err != nil {
		t.Fatalf("Update(no changes) error = %v", err)
	}
	if after := len(userLogRows(t, svc.database)); after != before {
		t.Fatalf("no-op update wrote %d rows", after-before)
	}

	invalid := "bogus"
	if _, err := svc.roles.Update(role.ID, UpdateRoleInput{Status: &invalid}, audit); !errors.Is(err, ErrInvalidRoleStatus) {
		t.Fatalf("Update(invalid status) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionUpdate, false, 0, admin.ID, "invalid role status")

	if _, err := svc.roles.Delete(role.ID, audit); err != nil {
		t.Fatalf("Delete(role) error = %v", err)
	}
	assertUserLog(t, lastUserLog(t, svc.database), model.UserLogActionDelete, true, 0, admin.ID, "deleted role: status: active -> deleted")
}
