package service

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/security"

	"gorm.io/gorm"
)

var ErrInvalidNodeStatus = errors.New("invalid node status")
var ErrNodeCommandNotFound = errors.New("node command not found")
var ErrNodeCommandOwnership = errors.New("node command ownership mismatch")

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
	NodeID   string        `json:"node_id"`
	Address  string        `json:"address"`
	LastSeen string        `json:"last_seen"`
	Commands []NodeCommand `json:"commands"`
}

type NodeCommand struct {
	ID        uint            `json:"id"`
	NodeID    string          `json:"node_id"`
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	LastError *string         `json:"last_error,omitempty"`
}

type UpdateNodeInput struct {
	Secret  *string
	Address *string
	Status  *string
}

type NodeService struct {
	nodes    *repository.NodeRepository
	commands *repository.CommandRepository
	mu       sync.RWMutex
	subs     map[string]map[chan NodeCommand]struct{}
}

func NewNodeService(nodes *repository.NodeRepository, commands *repository.CommandRepository) *NodeService {
	return &NodeService{
		nodes:    nodes,
		commands: commands,
		subs:     make(map[string]map[chan NodeCommand]struct{}),
	}
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

	commands, err := s.commands.ListPendingByNodeID(node.ID)
	if err != nil {
		return nil, err
	}

	return &NodeAvailabilityReport{
		NodeID:   node.ID,
		Address:  node.Address,
		LastSeen: now.Format(time.RFC3339),
		Commands: toNodeCommands(commands),
	}, nil
}

func (s *NodeService) CompleteCommand(nodeID string, commandID uint) (*NodeCommand, error) {
	command, err := s.commands.FindByID(commandID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeCommandNotFound
		}
		return nil, err
	}
	if command.NodeID != nodeID {
		return nil, ErrNodeCommandOwnership
	}
	if command.Status != model.NodeCommandStatusCompleted {
		command.Status = model.NodeCommandStatusCompleted
		if err := s.commands.Update(command); err != nil {
			return nil, err
		}
	}
	return toNodeCommand(command), nil
}

func (s *NodeService) Subscribe(nodeID string) (<-chan NodeCommand, func()) {
	ch := make(chan NodeCommand, 8)

	s.mu.Lock()
	if s.subs[nodeID] == nil {
		s.subs[nodeID] = make(map[chan NodeCommand]struct{})
	}
	s.subs[nodeID][ch] = struct{}{}
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		if subscribers, ok := s.subs[nodeID]; ok {
			delete(subscribers, ch)
			if len(subscribers) == 0 {
				delete(s.subs, nodeID)
			}
		}
		close(ch)
	}
}

func (s *NodeService) PublishCommand(command *model.Command) {
	nodeCommand := toNodeCommand(command)
	if nodeCommand == nil {
		return
	}

	s.mu.RLock()
	subscribers := make([]chan NodeCommand, 0, len(s.subs[command.NodeID]))
	for ch := range s.subs[command.NodeID] {
		subscribers = append(subscribers, ch)
	}
	s.mu.RUnlock()

	for _, ch := range subscribers {
		select {
		case ch <- *nodeCommand:
		default:
		}
	}
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

func toNodeCommands(commands []model.Command) []NodeCommand {
	if len(commands) == 0 {
		return []NodeCommand{}
	}

	result := make([]NodeCommand, 0, len(commands))
	for _, command := range commands {
		result = append(result, *toNodeCommand(&command))
	}
	return result
}

func toNodeCommand(command *model.Command) *NodeCommand {
	return &NodeCommand{
		ID:        command.ID,
		NodeID:    command.NodeID,
		Type:      string(command.Type),
		Status:    string(command.Status),
		Payload:   command.Payload,
		CreatedAt: command.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: command.UpdatedAt.UTC().Format(time.RFC3339),
		LastError: command.LastError,
	}
}
