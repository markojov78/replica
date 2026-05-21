package service

import (
	"errors"
	"time"

	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/security"

	"gorm.io/gorm"
)

var ErrInvalidNodeStatus = errors.New("invalid node status")

type NodeDetails struct {
	ID                  string  `json:"id"`
	Status              string  `json:"status"`
	Address             string  `json:"address"`
	LastSeen            *string `json:"last_seen,omitempty"`
	LastCallbackSuccess *string `json:"last_callback_success,omitempty"`
	LastCallbackFailure *string `json:"last_callback_failure,omitempty"`
}

type NodeList struct {
	Items []NodeDetails `json:"items"`
	Page  int           `json:"page"`
	Count int           `json:"count"`
	Total int64         `json:"total"`
}

type NodeAvailabilityReport struct {
	NodeID   string         `json:"node_id"`
	Address  string         `json:"address"`
	LastSeen string         `json:"last_seen"`
	Tasks    []NodeTaskStub `json:"tasks"`
}

type NodeTaskStub struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type UpdateNodeInput struct {
	Secret  *string
	Address *string
	Status  *string
}

type NodeService struct {
	nodes *repository.NodeRepository
}

func NewNodeService(nodes *repository.NodeRepository) *NodeService {
	return &NodeService{nodes: nodes}
}

func (s *NodeService) Create(id, secret, address string, status *string) (*NodeDetails, error) {
	hashedSecret, err := security.HashPassword(secret)
	if err != nil {
		return nil, err
	}

	nodeStatus := model.NodeStatusOffline
	if status != nil {
		nodeStatus = model.NodeStatus(*status)
		if !nodeStatus.Valid() {
			return nil, ErrInvalidNodeStatus
		}
	}

	node := &model.Node{
		ID:      id,
		Status:  nodeStatus,
		Secret:  hashedSecret,
		Address: address,
	}

	if err := s.nodes.Create(node); err != nil {
		return nil, err
	}

	return toNodeDetails(node), nil
}

func (s *NodeService) Get(id string) (*NodeDetails, error) {
	node, err := s.nodes.FindByID(id)
	if err != nil {
		return nil, err
	}

	return toNodeDetails(node), nil
}

func (s *NodeService) List(page, perPage int) (*NodeList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

	nodes, total, err := s.nodes.List(page, perPage)
	if err != nil {
		return nil, err
	}

	items := make([]NodeDetails, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, *toNodeDetails(&node))
	}

	return &NodeList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *NodeService) Update(id string, input UpdateNodeInput) (*NodeDetails, error) {
	node, err := s.nodes.FindByID(id)
	if err != nil {
		return nil, err
	}

	if input.Secret != nil {
		hashedSecret, err := security.HashPassword(*input.Secret)
		if err != nil {
			return nil, err
		}
		node.Secret = hashedSecret
	}

	if input.Address != nil {
		node.Address = *input.Address
	}

	if input.Status != nil {
		status := model.NodeStatus(*input.Status)
		if !status.Valid() {
			return nil, ErrInvalidNodeStatus
		}
		node.Status = status
	}

	if err := s.nodes.Update(node); err != nil {
		return nil, err
	}

	return toNodeDetails(node), nil
}

func (s *NodeService) Delete(id string) (*NodeDetails, error) {
	return s.Update(id, UpdateNodeInput{
		Status: stringPtr(string(model.NodeStatusRevoked)),
	})
}

func (s *NodeService) ReportAvailability(id, address string) (*NodeAvailabilityReport, error) {
	node, err := s.nodes.FindByID(id)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	node.Address = address
	node.LastSeen = &now

	if err := s.nodes.Update(node); err != nil {
		return nil, err
	}

	return &NodeAvailabilityReport{
		NodeID:   node.ID,
		Address:  node.Address,
		LastSeen: now.Format(time.RFC3339),
		Tasks:    []NodeTaskStub{},
	}, nil
}

func (s *NodeService) IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func toNodeDetails(node *model.Node) *NodeDetails {
	return &NodeDetails{
		ID:                  node.ID,
		Status:              string(node.Status),
		Address:             node.Address,
		LastSeen:            timePtr(node.LastSeen),
		LastCallbackSuccess: timePtr(node.LastCallbackSuccess),
		LastCallbackFailure: timePtr(node.LastCallbackFailure),
	}
}

func timePtr(value *time.Time) *string {
	if value == nil {
		return nil
	}

	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}
