package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"replica/internal/model"
	"testing"
)

func TestCommandCreationAuthorizationAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		action        model.PermissionAction
		resource      model.PermissionResource
		authenticated bool
		want          int
	}{
		{"unauthenticated", model.PermissionActionUpdate, model.PermissionResourceInventories, false, 401},
		{"reader", model.PermissionActionRead, model.PermissionResourceInventories, true, 403},
		{"updater", model.PermissionActionUpdate, model.PermissionResourceInventories, true, 201},
		{"node updater", model.PermissionActionUpdate, model.PermissionResourceNodes, true, 403},
		{"node creator", model.PermissionActionCreate, model.PermissionResourceNodes, true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openRouterTestDB(t)
			token := createConfigUserToken(t, db, []model.Permission{{Resource: tc.resource, Action: tc.action}})
			if !tc.authenticated {
				token = ""
			}
			node := model.Node{ID: "node-a", Status: model.NodeStatusOffline}
			inventory := model.Inventory{Name: "rescan", Type: model.InventoryTypeFolder, Status: model.InventoryStatusActive}
			if err := db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&inventory).Error; err != nil {
				t.Fatal(err)
			}
			replica := model.Replica{InventoryID: inventory.ID, NodeID: node.ID, URI: "/data/rescan", Type: model.ReplicaTypeFilesystem, Status: model.ReplicaStatusActive}
			if err := db.Create(&replica).Error; err != nil {
				t.Fatal(err)
			}
			handler := newConfigRouteHandler(db)
			response := adminJSONRequest(t, handler, http.MethodPost, "/api/admin/commands", map[string]any{"type": "scan_replica", "payload": map[string]any{"replica_id": replica.ID, "force_rescan": true}}, token)
			if response.Code != tc.want {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			var count int64
			if err := db.Model(&model.Command{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if tc.want != 201 {
				if count != 0 {
					t.Fatal("unauthorized request queued a command")
				}
				return
			}
			if count != 1 {
				t.Fatalf("commands = %d", count)
			}
			var cmd model.Command
			if err := json.Unmarshal(response.Body.Bytes(), &cmd); err != nil {
				t.Fatal(err)
			}
			if cmd.ID == 0 || cmd.NodeID != node.ID || cmd.Type != model.NodeCommandTypeScanReplica || cmd.Status != model.NodeCommandStatusPending {
				t.Fatalf("command = %+v", cmd)
			}
			for _, body := range []any{
				map[string]any{"type": "reconcile_replica", "payload": map[string]any{}},
				map[string]any{"type": "scan_replica", "payload": map[string]any{"replica_id": 0}},
				map[string]any{"type": "scan_replica", "payload": map[string]any{"replica_id": replica.ID, "force_rescan": "true"}},
			} {
				rejected := adminJSONRequest(t, handler, http.MethodPost, "/api/admin/commands", body, token)
				if rejected.Code != http.StatusBadRequest {
					t.Fatalf("invalid command status=%d body=%s", rejected.Code, rejected.Body.String())
				}
			}
			removed := adminRequest(t, handler, http.MethodPost, fmt.Sprintf("/api/admin/replicas/%d/rescan", replica.ID), nil, token)
			if removed.Code != http.StatusNotFound {
				t.Fatalf("removed endpoint status=%d", removed.Code)
			}
			removed = adminRequest(t, handler, http.MethodPost, fmt.Sprintf("/api/admin/replicas/%d/commands", replica.ID), nil, token)
			if removed.Code != http.StatusNotFound {
				t.Fatalf("removed replica commands endpoint status=%d", removed.Code)
			}
			missing := adminJSONRequest(t, handler, http.MethodPost, "/api/admin/commands", map[string]any{"type": "scan_replica", "payload": map[string]any{"replica_id": 99999}}, token)
			if missing.Code != http.StatusNotFound {
				t.Fatalf("missing replica status=%d body=%s", missing.Code, missing.Body.String())
			}

			if err := db.Model(&model.Command{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("invalid requests created commands: %d", count)
			}

		})
	}
}
