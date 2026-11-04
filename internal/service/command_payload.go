package service

import (
	"encoding/json"
	"errors"
)

var ErrInvalidCommandPayload = errors.New("invalid command payload")

// ScanReplicaCommandPayload is validated before authorization resolves the target inventory.
type ScanReplicaCommandPayload struct {
	ReplicaID   uint `json:"replica_id"`
	ForceRescan bool `json:"force_rescan"`
}

func ParseScanReplicaCommandPayload(raw json.RawMessage) (ScanReplicaCommandPayload, error) {
	var result ScanReplicaCommandPayload
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return result, ErrInvalidCommandPayload
	}
	for key, value := range fields {
		if string(value) == "null" {
			return result, ErrInvalidCommandPayload
		}
		switch key {
		case "replica_id":
			if err := json.Unmarshal(value, &result.ReplicaID); err != nil {
				return result, ErrInvalidCommandPayload
			}
		case "force_rescan":
			if err := json.Unmarshal(value, &result.ForceRescan); err != nil {
				return result, ErrInvalidCommandPayload
			}
		default:
			return result, ErrInvalidCommandPayload
		}
	}
	if result.ReplicaID == 0 {
		return result, ErrInvalidCommandPayload
	}
	return result, nil
}
