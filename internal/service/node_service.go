package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"

	"gorm.io/gorm"
)

var ErrInvalidNodeStatus = errors.New("invalid node status")
var ErrInvalidNodeCommandStatus = errors.New("invalid node command status")
var ErrInvalidNodeCommandType = errors.New("invalid node command type")
var ErrInvalidNodeCommandStatusTransition = errors.New("invalid node command status transition")
var ErrInvalidNodeCommandDateFilter = errors.New("invalid command date filter")
var ErrInvalidNodeCommandSort = errors.New("invalid command sort")
var ErrInvalidNodeCommandOrder = errors.New("invalid command order")
var ErrNodeCommandNotFound = errors.New("node command not found")
var ErrNodeCommandOwnership = errors.New("node command ownership mismatch")

const nodeStatusCheckInterval = 5 * time.Second

type NodeDetails struct {
	ID             string   `json:"id"`
	Status         string   `json:"status"`
	Address        string   `json:"address"`
	SharingEnabled bool     `json:"sharing_enabled"`
	Interval       *float64 `json:"interval,omitempty"`
	LastSeen       *string  `json:"last_seen,omitempty"`
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
	LastError *string         `json:"last_error"`
}

type NodeCommandList struct {
	Items []NodeCommand `json:"items"`
	Page  int           `json:"page"`
	Count int           `json:"count"`
	Total int64         `json:"total"`
}

type NodeCommandListFilter struct {
	NodeID        string
	Type          string
	Status        string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Sort          string
	Order         string
}

type UpdateNodeInput struct {
	Secret                   *string
	Address                  *string
	Status                   *string
	SharingEnabled           *bool
	skipRefreshConfigCommand bool
}

type UpdateNodeCommandInput struct {
	Status string
	Error  *string
}

type NodeService struct {
	nodes       *repository.NodeRepository
	commands    *repository.CommandRepository
	nodeTokens  *repository.NodeTokenRepository
	statusMu    sync.Mutex
	mu          sync.RWMutex
	subs        map[string]map[chan NodeCommand]struct{}
	connections map[string]int
}

func NewNodeService(nodes *repository.NodeRepository, commands *repository.CommandRepository, nodeTokens ...*repository.NodeTokenRepository) *NodeService {
	s := &NodeService{
		nodes:       nodes,
		commands:    commands,
		subs:        make(map[string]map[chan NodeCommand]struct{}),
		connections: make(map[string]int),
	}
	if len(nodeTokens) > 0 {
		s.nodeTokens = nodeTokens[0]
	}
	return s
}

func (s *NodeService) Start(ctx context.Context) {
	go func() {
		if err := s.ReconcileStatuses(time.Now().UTC()); err != nil {
			log.Printf("reconcile node statuses: %v", err)
		}

		ticker := time.NewTicker(nodeStatusCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := s.ReconcileStatuses(now.UTC()); err != nil {
					log.Printf("reconcile node statuses: %v", err)
				}
			}
		}
	}()
}

func (s *NodeService) Create(id, secret, address string, status *string, sharingEnabled bool) (*NodeDetails, error) {
	hashedSecret, err := security.HashPassword(secret)
	if err != nil {
		return nil, err
	}

	nodeStatus := model.NodeStatusOffline
	if status != nil {
		nodeStatus = model.NodeStatus(*status)
		if !adminSettableNodeStatus(nodeStatus) {
			return nil, ErrInvalidNodeStatus
		}
	}

	node := &model.Node{
		ID:      id,
		Status:  nodeStatus,
		Secret:  hashedSecret,
		Address: address,
		Sharing: sharingEnabled,
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
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	node, err := s.nodes.FindByID(id)
	if err != nil {
		return nil, err
	}

	deleteToken := input.Secret != nil

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
		if !validAdminNodeStatusTransition(node.Status, status) {
			return nil, ErrInvalidNodeStatus
		}
		if status == model.NodeStatusDisabled || status == model.NodeStatusRevoked {
			deleteToken = true
		}
		node.Status = status
		if status == model.NodeStatusOffline {
			s.applyAutomaticStatus(node, time.Now().UTC())
		}
	}

	if input.SharingEnabled != nil {
		node.Sharing = *input.SharingEnabled
	}

	if err := s.nodes.Update(node); err != nil {
		return nil, err
	}

	if deleteToken && s.nodeTokens != nil {
		if err := s.nodeTokens.DeleteByNodeID(node.ID); err != nil {
			return nil, err
		}
	}

	if !input.skipRefreshConfigCommand {
		command := &model.Command{
			NodeID:  node.ID,
			Type:    model.NodeCommandTypeRefreshConfig,
			Status:  model.NodeCommandStatusPending,
			Payload: json.RawMessage(`{}`),
		}
		if err := s.commands.Create(command); err != nil {
			return nil, err
		}
		s.PublishCommand(command)
	}

	return toNodeDetails(node), nil
}

func (s *NodeService) Delete(id string) (*NodeDetails, error) {
	return s.Update(id, UpdateNodeInput{
		Status:                   stringPtr(string(model.NodeStatusRevoked)),
		skipRefreshConfigCommand: true,
	})
}

func (s *NodeService) ReportAvailability(id, address string, interval float64) (*NodeAvailabilityReport, error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	node, err := s.nodes.FindByID(id)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	node.Address = address
	node.Interval = &interval
	node.LastSeen = &now
	s.applyAutomaticStatus(node, now)

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

func (s *NodeService) WebSocketConnected(nodeID string) error {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	s.mu.Lock()
	s.connections[nodeID]++
	s.mu.Unlock()

	if err := s.reconcileStatus(nodeID, time.Now().UTC()); err != nil {
		s.mu.Lock()
		s.connections[nodeID]--
		if s.connections[nodeID] == 0 {
			delete(s.connections, nodeID)
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *NodeService) WebSocketDisconnected(nodeID string) error {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	s.mu.Lock()
	if s.connections[nodeID] > 1 {
		s.connections[nodeID]--
	} else {
		delete(s.connections, nodeID)
	}
	s.mu.Unlock()

	return s.reconcileStatus(nodeID, time.Now().UTC())
}

func (s *NodeService) ReconcileStatuses(now time.Time) error {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	nodes, err := s.nodes.ListAll()
	if err != nil {
		return err
	}

	for i := range nodes {
		node := &nodes[i]
		previous := node.Status
		s.applyAutomaticStatus(node, now)
		if node.Status != previous {
			if err := s.nodes.Update(node); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *NodeService) reconcileStatus(nodeID string, now time.Time) error {
	node, err := s.nodes.FindByID(nodeID)
	if err != nil {
		return err
	}

	previous := node.Status
	s.applyAutomaticStatus(node, now)
	if node.Status == previous {
		return nil
	}
	return s.nodes.Update(node)
}

func (s *NodeService) applyAutomaticStatus(node *model.Node, now time.Time) {
	if node.Status == model.NodeStatusDisabled || node.Status == model.NodeStatusRevoked {
		return
	}
	if s.hasActiveWebSocket(node.ID) {
		node.Status = model.NodeStatusOnline
		return
	}
	if node.Interval == nil || *node.Interval <= 0 || node.LastSeen == nil {
		node.Status = model.NodeStatusOffline
		return
	}
	if now.Sub(*node.LastSeen).Seconds() <= 2**node.Interval {
		node.Status = model.NodeStatusUnreachable
		return
	}
	node.Status = model.NodeStatusOffline
}

func (s *NodeService) hasActiveWebSocket(nodeID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connections[nodeID] > 0
}

func (s *NodeService) UpdateCommand(nodeID string, commandID uint, input UpdateNodeCommandInput) (*NodeCommand, error) {
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

	status := model.CommandStatus(input.Status)
	if !status.Valid() {
		return nil, ErrInvalidNodeCommandStatus
	}

	command.Status = status
	command.LastError = input.Error
	if err := s.commands.Update(command); err != nil {
		return nil, err
	}

	return toNodeCommand(command), nil
}

func (s *NodeService) ListCommands(page, perPage int, filter NodeCommandListFilter) (*NodeCommandList, error) {
	commandType := model.CommandType(filter.Type)
	if commandType != "" && !commandType.Valid() {
		return nil, ErrInvalidNodeCommandType
	}
	status := model.CommandStatus(filter.Status)
	if status != "" && !status.Valid() {
		return nil, ErrInvalidNodeCommandStatus
	}
	if filter.CreatedAfter != nil && filter.CreatedBefore != nil && !filter.CreatedAfter.Before(*filter.CreatedBefore) {
		return nil, ErrInvalidNodeCommandDateFilter
	}
	if filter.Sort == "" {
		filter.Sort = "id"
	}
	switch filter.Sort {
	case "id", "created_at", "updated_at":
	default:
		return nil, ErrInvalidNodeCommandSort
	}
	if filter.Order == "" {
		filter.Order = "asc"
	}
	if filter.Order != "asc" && filter.Order != "desc" {
		return nil, ErrInvalidNodeCommandOrder
	}

	commands, total, err := s.commands.List(page, perPage, repository.CommandListFilter{
		NodeID: filter.NodeID, Type: commandType, Status: status,
		CreatedAfter: filter.CreatedAfter, CreatedBefore: filter.CreatedBefore,
		Sort: filter.Sort, Order: filter.Order,
	})
	if err != nil {
		return nil, err
	}
	return &NodeCommandList{Items: toNodeCommands(commands), Page: page, Count: perPage, Total: total}, nil
}

func (s *NodeService) GetCommand(commandID uint) (*NodeCommand, error) {
	command, err := s.commands.FindByID(commandID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeCommandNotFound
		}
		return nil, err
	}
	return toNodeCommand(command), nil
}

func (s *NodeService) UpdateCommandStatus(commandID uint, statusValue string) (*NodeCommand, error) {
	status := model.CommandStatus(statusValue)
	if !status.Valid() {
		return nil, ErrInvalidNodeCommandStatus
	}
	command, err := s.commands.FindByID(commandID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeCommandNotFound
		}
		return nil, err
	}
	if !validAdminCommandStatusTransition(command.Status, status) {
		return nil, ErrInvalidNodeCommandStatusTransition
	}

	command.Status = status
	if err := s.commands.Update(command); err != nil {
		return nil, err
	}
	if status == model.NodeCommandStatusPending {
		s.PublishCommand(command)
	}
	return toNodeCommand(command), nil
}

func validAdminCommandStatusTransition(current, next model.CommandStatus) bool {
	switch current {
	case model.NodeCommandStatusFailed:
		return next == model.NodeCommandStatusPending || next == model.NodeCommandStatusCanceled
	case model.NodeCommandStatusPending:
		return next == model.NodeCommandStatusCompleted || next == model.NodeCommandStatusCanceled
	default:
		return false
	}
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
		ID:             node.ID,
		Status:         string(node.Status),
		Address:        node.Address,
		SharingEnabled: node.Sharing,
		Interval:       node.Interval,
		LastSeen:       timePtr(node.LastSeen),
	}
}

func adminSettableNodeStatus(status model.NodeStatus) bool {
	switch status {
	case model.NodeStatusOffline, model.NodeStatusDisabled, model.NodeStatusRevoked:
		return true
	default:
		return false
	}
}

func validAdminNodeStatusTransition(current, next model.NodeStatus) bool {
	switch next {
	case model.NodeStatusDisabled, model.NodeStatusRevoked:
		return true
	case model.NodeStatusOffline:
		return current == model.NodeStatusOffline || current == model.NodeStatusDisabled || current == model.NodeStatusRevoked
	default:
		return false
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
