package repository_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"
)

func TestExpireAnonymousAccess(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "cleanup"
		if rollback {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "shares.db")})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(database); err != nil {
				t.Fatal(err)
			}
			repo := repository.NewShareRepository(database)
			replica := model.Replica{NodeID: "node-a", InventoryID: 1, URI: "/photos", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
			if err := database.Create(&replica).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			past, future := now.Add(-time.Hour), now.Add(time.Hour)
			userID := uint(1)
			userPermissions := []repository.UserPermissionDetails{{UserID: userID, Permissions: []string{"read", "update"}}}
			cases := []struct {
				expiration *time.Time
				hash       bool
				anonymous  bool
				expired    bool
			}{
				{&past, true, true, true},
				{&now, true, true, true},
				{&future, true, true, false},
				{nil, true, true, false},
				{&past, false, true, true},
				{&past, true, false, true},
				{&past, false, false, false},
			}
			shares := make([]model.Share, len(cases))
			for i, tc := range cases {
				hash := "public-link"
				share := model.Share{ReplicaID: replica.ID, Name: "Photos", Status: model.ShareStatusActive, ShareExpiration: tc.expiration}
				if tc.hash {
					share.LinkHash = &hash
				}
				var anonymous []string
				if tc.anonymous {
					anonymous = []string{"read", "delete"}
				}
				if err := repo.CreateWithPermissions(&share, userPermissions, anonymous, nil); err != nil {
					t.Fatal(err)
				}
				shares[i] = share
			}
			if rollback {
				if err := database.Exec("CREATE TRIGGER fail_refresh BEFORE INSERT ON commands BEGIN SELECT RAISE(ABORT, 'refresh failed'); END").Error; err != nil {
					t.Fatal(err)
				}
			}
			commands, err := repo.ExpireAnonymousAccess(now)
			if rollback {
				if err == nil || len(commands) != 0 {
					t.Fatalf("expected rollback, got commands=%v err=%v", commands, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(commands) != 4 {
					t.Fatalf("commands = %d, want 4", len(commands))
				}
				for _, command := range commands {
					if command.ID == 0 || command.NodeID != replica.NodeID || command.Type != model.NodeCommandTypeRefreshState || command.Status != model.NodeCommandStatusPending {
						t.Fatalf("invalid refresh command: %+v", command)
					}
				}
			}
			for i, tc := range cases {
				share, err := repo.FindByID(shares[i].ID)
				if err != nil {
					t.Fatal(err)
				}
				cleared := tc.expired && !rollback
				if (share.LinkHash != nil) != (tc.hash && !cleared) {
					t.Fatalf("case %d: unexpected hash %v", i, share.LinkHash)
				}
				wantExpiration := tc.expiration
				if cleared {
					wantExpiration = nil
				}
				if !reflect.DeepEqual(share.ShareExpiration, wantExpiration) {
					t.Fatalf("case %d: expiration = %v, want %v", i, share.ShareExpiration, wantExpiration)
				}
				if share.Status != shares[i].Status || share.Name != shares[i].Name {
					t.Fatalf("case %d: unrelated share fields changed", i)
				}
				permissions, err := repo.UserPermissions(share.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(permissions, userPermissions) {
					t.Fatalf("case %d: user permissions changed: %v", i, permissions)
				}
				var anonymousCount int64
				if err := database.Model(&model.ShareUser{}).Where("share_id = ? AND anonymous = ?", share.ID, true).Count(&anonymousCount).Error; err != nil {
					t.Fatal(err)
				}
				want := int64(0)
				if tc.anonymous && !cleared {
					want = 1
				}
				if anonymousCount != want {
					t.Fatalf("case %d: anonymous rows = %d, want %d", i, anonymousCount, want)
				}
			}
			var orphanCount int64
			if err := database.Model(&model.SharePermission{}).Where("share_user_id NOT IN (SELECT id FROM share_users)").Count(&orphanCount).Error; err != nil {
				t.Fatal(err)
			}
			if orphanCount != 0 {
				t.Fatalf("orphan permissions = %d", orphanCount)
			}
			if !rollback {
				commands, err := repo.ExpireAnonymousAccess(now)
				if err != nil || len(commands) != 0 {
					t.Fatalf("repeat cleanup commands=%v err=%v", commands, err)
				}
			}
			var count int64
			if err := database.Model(&model.Command{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			want := int64(4)
			if rollback {
				want = 0
			}
			if count != want {
				t.Fatalf("stored commands = %d, want %d", count, want)
			}
		})
	}
}
