package service

import (
	"dropoutbox/internal/config"
)

type NodeSummary struct {
	Name        string `json:"name"`
	NodeID      string `json:"node_id"`
	Coordinator bool   `json:"coordinator"`
	Storage     bool   `json:"storage"`
}

type NodeService struct {
	cfg config.Config
}

func NewNodeService(cfg config.Config) *NodeService {
	return &NodeService{cfg: cfg}
}

func (s *NodeService) Summary() NodeSummary {
	return NodeSummary{
		Name:        "DropOutBox",
		NodeID:      s.cfg.App.NodeID,
		Coordinator: s.cfg.App.Coordinator,
		Storage:     s.cfg.App.Storage,
	}
}
