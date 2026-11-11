package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"replica/internal/apiclient"
	"replica/internal/service"
)

const (
	// nodeLogUploadBatchLines is the number of lines sent in one upload.
	nodeLogUploadBatchLines = 1000
	// nodeLogPendingLimit caps events held in memory before the first heartbeat sets the id counter.
	nodeLogPendingLimit = 10000
	// nodeLogMaxLineBytes bounds a single line when reading the buffer file.
	nodeLogMaxLineBytes = 1 << 20
	// nodeLogRejectedLogLimit caps how many rejected lines of one upload are written to the app log.
	nodeLogRejectedLogLimit = 20
)

type nodeLogUploader interface {
	UploadLogs(ctx context.Context, lines []byte) (*apiclient.LogUploadResult, error)
}

// nodeLogBuffer is the storage-node log buffer: a local JSON lines file that is uploaded to the
// coordinator in batches. Each line has a node-assigned id, unique per node across all log
// types. The buffer holds no authoritative state: the coordinator's log cursor decides where
// ids continue and which lines are stored. A nil buffer discards events.
type nodeLogBuffer struct {
	mu   sync.Mutex
	path string
	file *os.File

	// ready is set by the first heartbeat that carries the coordinator log cursor.
	ready bool
	// lastID is the last id assigned to a line.
	lastID uint64
	// confirmed is the highest id the coordinator has processed; lines up to it can be removed.
	confirmed uint64
	uploading bool

	pending        []service.ShareAccessLogLine
	droppedPending int
}

func newNodeLogBuffer(path string) *nodeLogBuffer {
	if path == "" {
		return nil
	}
	return &nodeLogBuffer{path: path}
}

// RecordShareAccess appends a share access line. Before the id counter is known, lines are held
// in memory up to nodeLogPendingLimit. Logging is best-effort: failures never block access.
func (b *nodeLogBuffer) RecordShareAccess(line service.ShareAccessLogLine) {
	if b == nil {
		return
	}
	line.Type = service.NodeLogTypeShareAccess
	if line.Timestamp.IsZero() {
		line.Timestamp = time.Now().UTC()
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ready {
		if len(b.pending) >= nodeLogPendingLimit {
			b.droppedPending++
			return
		}
		b.pending = append(b.pending, line)
		return
	}
	b.appendLocked(line)
}

func (b *nodeLogBuffer) appendLocked(line service.ShareAccessLogLine) {
	b.lastID++
	line.ID = b.lastID
	data, err := json.Marshal(line)
	if err != nil {
		log.Printf("node log: encode line id=%d: %v", line.ID, err)
		return
	}
	if b.file == nil {
		file, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Printf("node log: open %s: %v", b.path, err)
			return
		}
		b.file = file
	}
	if _, err := b.file.Write(append(data, '\n')); err != nil {
		log.Printf("node log: write %s: %v", b.path, err)
	}
}

// ObserveCoordinator applies the log cursor from a heartbeat response. The first cursor sets the
// id counter to the higher of the coordinator cursor and the last id in the local file, and
// flushes the events held in memory. Later cursors only lower the confirmed id when the
// coordinator lost rows (for example after a database restore), so those lines are re-sent
// while the local file still has them. Cursors are ignored while an upload is in flight,
// because they may predate it.
func (b *nodeLogBuffer) ObserveCoordinator(status *apiclient.LogsStatus) {
	if b == nil || status == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.ready {
		localLast, err := b.localLastIDLocked()
		if err != nil {
			log.Printf("node log: read %s: %v", b.path, err)
		}
		b.lastID = max(localLast, status.LastID)
		b.confirmed = status.LastID
		b.ready = true
		for _, line := range b.pending {
			b.appendLocked(line)
		}
		b.pending = nil
		if b.droppedPending > 0 {
			log.Printf("node log: dropped %d events received before the coordinator log cursor was known", b.droppedPending)
			b.droppedPending = 0
		}
		return
	}
	if b.uploading {
		return
	}
	if status.LastID < b.confirmed {
		log.Printf("node log: coordinator log cursor %d is behind confirmed id %d; re-sending buffered lines", status.LastID, b.confirmed)
		b.confirmed = status.LastID
	}
}

// UploadOnce sends one batch of unconfirmed lines. It reports whether more lines are waiting.
// Rejected lines are logged and not retried; only transport and coordinator errors return an
// error, and the same lines are sent again on the next attempt.
func (b *nodeLogBuffer) UploadOnce(ctx context.Context, uploader nodeLogUploader) (bool, error) {
	if b == nil {
		return false, nil
	}
	b.mu.Lock()
	if !b.ready {
		b.mu.Unlock()
		return false, nil
	}
	batch, count, more, err := b.readBatchLocked(nodeLogUploadBatchLines)
	if err != nil || count == 0 {
		b.mu.Unlock()
		return false, err
	}
	b.uploading = true
	b.mu.Unlock()

	result, err := uploader.UploadLogs(ctx, batch)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.uploading = false
	if err != nil {
		return false, err
	}
	for i, rejected := range result.Rejected {
		if i == nodeLogRejectedLogLimit {
			log.Printf("node log: %d more lines rejected", len(result.Rejected)-i)
			break
		}
		log.Printf("node log: coordinator rejected line id=%d: %s", rejected.ID, rejected.Error)
	}
	b.confirmed = max(b.confirmed, result.LastID)
	if err := b.compactLocked(); err != nil {
		log.Printf("node log: compact %s: %v", b.path, err)
	}
	return more, nil
}

// Run uploads buffered lines every interval until ctx is done.
func (b *nodeLogBuffer) Run(ctx context.Context, uploader nodeLogUploader, interval time.Duration) {
	if b == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer b.close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				more, err := b.UploadOnce(ctx, uploader)
				if err != nil {
					log.Printf("node log: upload failed: %v", err)
					break
				}
				if !more {
					break
				}
			}
		}
	}
}

func (b *nodeLogBuffer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}
}

// scanLocked calls fn for every line of the buffer file with its id; unreadable lines get id 0.
func (b *nodeLogBuffer) scanLocked(fn func(id uint64, line []byte) bool) error {
	file, err := os.Open(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), nodeLogMaxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var header struct {
			ID uint64 `json:"id"`
		}
		if json.Unmarshal(line, &header) != nil {
			header.ID = 0
		}
		if !fn(header.ID, line) {
			return nil
		}
	}
	return scanner.Err()
}

func (b *nodeLogBuffer) localLastIDLocked() (uint64, error) {
	var last uint64
	err := b.scanLocked(func(id uint64, _ []byte) bool {
		last = max(last, id)
		return true
	})
	return last, err
}

// readBatchLocked returns up to limit unconfirmed lines as NDJSON.
func (b *nodeLogBuffer) readBatchLocked(limit int) ([]byte, int, bool, error) {
	var batch []byte
	count, more := 0, false
	err := b.scanLocked(func(id uint64, line []byte) bool {
		if id <= b.confirmed {
			return true
		}
		if count == limit {
			more = true
			return false
		}
		batch = append(batch, line...)
		batch = append(batch, '\n')
		count++
		return true
	})
	return batch, count, more, err
}

// compactLocked rewrites the buffer file without confirmed and unreadable lines.
func (b *nodeLogBuffer) compactLocked() error {
	tmpPath := b.path + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(tmp)
	dropped := 0
	var writeErr error
	err = b.scanLocked(func(id uint64, line []byte) bool {
		if id == 0 {
			dropped++
			return true
		}
		if id <= b.confirmed {
			return true
		}
		if _, writeErr = writer.Write(line); writeErr == nil {
			writeErr = writer.WriteByte('\n')
		}
		return writeErr == nil
	})
	if err == nil {
		err = writeErr
	}
	if err == nil {
		err = writer.Flush()
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}
	if err := os.Rename(tmpPath, b.path); err != nil {
		return fmt.Errorf("replace buffer file: %w", err)
	}
	if dropped > 0 {
		log.Printf("node log: removed %d unreadable lines from %s", dropped, b.path)
	}
	return nil
}
