package service

import (
	"encoding/json"
	"errors"
	"replica/internal/model"
	"testing"
)

func TestReplicaCommandQueuesForcedScanForBaseAndDownstream(t *testing.T) {
	f := newShareFileReportFixture(t)
	delivered, unsubscribe := f.svc.nodes.Subscribe("node-a")
	defer unsubscribe()
	for _, replica := range []*model.Replica{f.replica, f.downstream} {
		cmd, err := f.svc.CreateScanCommand(ScanReplicaCommandPayload{ReplicaID: replica.ID, ForceRescan: true})
		if err != nil {
			t.Fatal(err)
		}
		var stored model.Command
		if err := f.database.First(&stored, cmd.ID).Error; err != nil {
			t.Fatal(err)
		}
		var payload struct {
			ReplicaID   uint `json:"replica_id"`
			ForceRescan bool `json:"force_rescan"`
		}
		if err := json.Unmarshal(stored.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if stored.NodeID != replica.NodeID || stored.Type != model.NodeCommandTypeScanReplica || stored.Status != model.NodeCommandStatusPending || payload.ReplicaID != replica.ID || !payload.ForceRescan {
			t.Fatalf("unexpected command: %+v %+v", stored, payload)
		}
		select {
		case got := <-delivered:
			if got.ID != cmd.ID {
				t.Fatalf("published command = %+v", got)
			}
		default:
			t.Fatal("command not published")
		}
	}
}

func TestReplicaCommandRejectsDeletedState(t *testing.T) {
	f := newShareFileReportFixture(t)
	if err := f.database.Model(f.replica).Update("status", model.ReplicaStatusDeleted).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateScanCommand(ScanReplicaCommandPayload{ReplicaID: f.replica.ID}); !errors.Is(err, ErrInvalidReplicaStatus) {
		t.Fatalf("error = %v", err)
	}
	if err := f.database.Model(&model.Inventory{}).Where("id = ?", f.replica.InventoryID).Update("status", model.InventoryStatusDeleted).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateScanCommand(ScanReplicaCommandPayload{ReplicaID: f.downstream.ID}); !errors.Is(err, ErrInventoryDeleted) {
		t.Fatalf("error = %v", err)
	}
	var count int64
	if err := f.database.Model(&model.Command{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected scans queued %d commands", count)
	}
}

func TestScanReplicaCommandPayloadValidation(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid, force  bool
	}{
		{"default", `{"replica_id":7}`, true, false},
		{"ordinary", `{"replica_id":7,"force_rescan":false}`, true, false},
		{"forced", `{"replica_id":7,"force_rescan":true}`, true, true},
		{"missing payload", ``, false, false},
		{"null", `null`, false, false},
		{"array", `[]`, false, false},
		{"missing replica", `{"force_rescan":true}`, false, false},
		{"zero replica", `{"replica_id":0}`, false, false},
		{"negative replica", `{"replica_id":-1}`, false, false},
		{"string replica", `{"replica_id":"7"}`, false, false},
		{"null replica", `{"replica_id":null}`, false, false},
		{"null flag", `{"replica_id":7,"force_rescan":null}`, false, false},
		{"string flag", `{"replica_id":7,"force_rescan":"true"}`, false, false},
		{"node override", `{"replica_id":7,"node_id":"node-b"}`, false, false},
		{"unknown option", `{"replica_id":7,"force":true}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseScanReplicaCommandPayload(json.RawMessage(tc.payload))
			if !tc.valid {
				if !errors.Is(err, ErrInvalidCommandPayload) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ReplicaID != 7 || got.ForceRescan != tc.force {
				t.Fatalf("payload = %+v", got)
			}
		})
	}
}
