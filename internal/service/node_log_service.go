package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"replica/internal/model"
	"replica/internal/repository"
)

// NodeLogTypeShareAccess is the node log line type stored in share_access_logs.
const NodeLogTypeShareAccess = "share_access"

const (
	// NodeLogMaxLines is the maximum number of lines accepted in one upload.
	NodeLogMaxLines = 5000
	// NodeLogMaxBytes is the maximum decompressed size of one upload.
	NodeLogMaxBytes = 10 << 20

	nodeLogPathMaxBytes = 1024
)

var (
	ErrNodeLogBatchTooLarge = errors.New("log batch too large")
	ErrInvalidNodeLogBody   = errors.New("invalid log body")
)

// NodeLogsStatus is returned to storage nodes in the heartbeat response.
type NodeLogsStatus struct {
	LastID uint64 `json:"last_id"`
}

// NodeLogRejection describes an uploaded line that was not stored.
type NodeLogRejection struct {
	ID    uint64 `json:"id,omitempty"`
	Line  int    `json:"line"`
	Error string `json:"error"`
}

// NodeLogIngestResult is the response to a node log upload.
type NodeLogIngestResult struct {
	LastID   uint64             `json:"last_id"`
	Rejected []NodeLogRejection `json:"rejected"`
}

// ShareAccessLogLine is one share_access line of the storage-node log buffer and upload.
type ShareAccessLogLine struct {
	ID            uint64    `json:"id"`
	Type          string    `json:"type"`
	Timestamp     time.Time `json:"timestamp"`
	Action        string    `json:"action"`
	Success       bool      `json:"success"`
	Message       string    `json:"message,omitempty"`
	ShareID       *uint     `json:"share_id,omitempty"`
	FileID        *uint     `json:"file_id,omitempty"`
	Path          string    `json:"path,omitempty"`
	UserID        *uint     `json:"user_id,omitempty"`
	Anonymous     bool      `json:"anonymous"`
	Source        string    `json:"source"`
	Bytes         *int64    `json:"bytes,omitempty"`
	SourceAddress string    `json:"source_address,omitempty"`
}

// NodeLogService stores log lines uploaded by storage nodes.
type NodeLogService struct {
	repo *repository.NodeLogRepository
	now  func() time.Time
}

func NewNodeLogService(repo *repository.NodeLogRepository) *NodeLogService {
	return &NodeLogService{repo: repo, now: time.Now}
}

// LastID returns the highest log id processed for the node.
func (s *NodeLogService) LastID(nodeID string) (uint64, error) {
	return s.repo.LastID(nodeID)
}

// Ingest stores an NDJSON log upload from nodeID. Invalid and duplicate lines are rejected
// individually and reported in the result; the other lines are stored. The node id always
// comes from the caller, never from the lines.
func (s *NodeLogService) Ingest(nodeID string, body io.Reader) (*NodeLogIngestResult, error) {
	reader := bufio.NewReader(io.LimitReader(body, NodeLogMaxBytes+1))
	now := s.now().UTC()
	result := &NodeLogIngestResult{Rejected: []NodeLogRejection{}}
	var rows []model.ShareAccessLog
	lineByID := map[uint64]int{}
	var maxID uint64
	total, lineNumber := 0, 0

	for {
		raw, err := reader.ReadBytes('\n')
		total += len(raw)
		if total > NodeLogMaxBytes {
			return nil, ErrNodeLogBatchTooLarge
		}
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 {
			lineNumber++
			if lineNumber > NodeLogMaxLines {
				return nil, ErrNodeLogBatchTooLarge
			}
			id, row, lineErr := parseNodeLogLine(nodeID, trimmed, now)
			if id > maxID {
				maxID = id
			}
			if lineErr == nil {
				if _, seen := lineByID[id]; seen {
					lineErr = errors.New("duplicate id")
				}
			}
			if lineErr != nil {
				result.Rejected = append(result.Rejected, NodeLogRejection{ID: id, Line: lineNumber, Error: lineErr.Error()})
			} else {
				lineByID[id] = lineNumber
				rows = append(rows, row)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidNodeLogBody, err)
		}
	}

	duplicates, lastID, err := s.repo.StoreBatch(nodeID, rows, maxID, now)
	if err != nil {
		return nil, err
	}
	for _, id := range duplicates {
		result.Rejected = append(result.Rejected, NodeLogRejection{ID: id, Line: lineByID[id], Error: "duplicate id"})
	}
	sort.Slice(result.Rejected, func(i, j int) bool { return result.Rejected[i].Line < result.Rejected[j].Line })
	result.LastID = lastID
	return result, nil
}

// parseNodeLogLine validates one line. It returns the line id whenever it can be read, so
// rejected lines still advance the node cursor.
func parseNodeLogLine(nodeID string, raw []byte, receivedAt time.Time) (uint64, model.ShareAccessLog, error) {
	var header struct {
		ID   uint64 `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return 0, model.ShareAccessLog{}, errors.New("invalid JSON")
	}
	if header.ID == 0 {
		return 0, model.ShareAccessLog{}, errors.New("missing id")
	}
	switch header.Type {
	case NodeLogTypeShareAccess:
		row, err := parseShareAccessLogLine(nodeID, raw, receivedAt)
		return header.ID, row, err
	default:
		return header.ID, model.ShareAccessLog{}, fmt.Errorf("unknown type %q", header.Type)
	}
}

func parseShareAccessLogLine(nodeID string, raw []byte, receivedAt time.Time) (model.ShareAccessLog, error) {
	var line ShareAccessLogLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return model.ShareAccessLog{}, errors.New("invalid share_access line")
	}
	action := model.ShareAccessLogAction(line.Action)
	if !action.Valid() {
		return model.ShareAccessLog{}, fmt.Errorf("invalid action %q", line.Action)
	}
	source := model.ShareAccessLogSource(line.Source)
	if !source.Valid() {
		return model.ShareAccessLog{}, fmt.Errorf("invalid source %q", line.Source)
	}
	if line.Timestamp.IsZero() {
		return model.ShareAccessLog{}, errors.New("missing timestamp")
	}
	if line.Bytes != nil && *line.Bytes < 0 {
		return model.ShareAccessLog{}, errors.New("invalid bytes")
	}
	return model.ShareAccessLog{
		ID:            line.ID,
		NodeID:        nodeID,
		Timestamp:     line.Timestamp.UTC(),
		Action:        action,
		Success:       line.Success,
		Message:       optionalString(truncateUTF8(line.Message, userLogMessageMaxBytes)),
		ShareID:       line.ShareID,
		FileID:        line.FileID,
		Path:          optionalString(truncateUTF8(line.Path, nodeLogPathMaxBytes)),
		UserID:        line.UserID,
		Anonymous:     line.Anonymous,
		Source:        source,
		Bytes:         line.Bytes,
		SourceAddress: optionalString(truncateUTF8(strings.TrimSpace(line.SourceAddress), 255)),
		ReceivedAt:    receivedAt,
	}, nil
}
