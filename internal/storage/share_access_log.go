package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"replica/internal/apiclient"
	"replica/internal/model"
	"replica/internal/service"
)

// shareAccessErrorCaptureBytes is how much of an error response is kept to read its message.
const shareAccessErrorCaptureBytes = 512

// shareAccessInfo collects request details that are known only inside the share handler.
type shareAccessInfo struct {
	userID *uint
	path   string
	bytes  *int64
}

type shareAccessInfoKey struct{}

func shareAccessInfoFrom(ctx context.Context) *shareAccessInfo {
	info, _ := ctx.Value(shareAccessInfoKey{}).(*shareAccessInfo)
	return info
}

// NoteShareAccessUser records the authenticated share user for the access log of the request.
func NoteShareAccessUser(ctx context.Context, userID uint) {
	if info := shareAccessInfoFrom(ctx); info != nil {
		info.userID = &userID
	}
}

// noteShareAccessFile records the file path and transferred size of an upload or replacement.
func noteShareAccessFile(ctx context.Context, path string, size int64) {
	if info := shareAccessInfoFrom(ctx); info != nil {
		if path != "" {
			info.path = path
		}
		if size >= 0 {
			info.bytes = &size
		}
	}
}

// ShareAccessLog wraps a share API or share UI handler and records one share access log line per
// request. anonymous marks public link routes, whose share is resolved from the link_hash path
// value; the link hash itself is never logged.
func (r *Runtime) ShareAccessLog(action model.ShareAccessLogAction, anonymous bool, next http.HandlerFunc) http.HandlerFunc {
	if r == nil || r.nodeLogs == nil {
		return next
	}
	return func(w http.ResponseWriter, req *http.Request) {
		line := service.ShareAccessLogLine{
			Timestamp:     time.Now().UTC(),
			Action:        string(action),
			Anonymous:     anonymous,
			Source:        string(model.ShareAccessLogSourceHTTP),
			SourceAddress: service.SourceAddress(req.Context()),
		}
		if anonymous {
			if share, ok := r.shareByLinkHash(strings.TrimSpace(req.PathValue("link_hash"))); ok {
				line.ShareID = uintPointer(share.ID)
			}
		} else if shareID, err := strconv.ParseUint(req.PathValue("id"), 10, 64); err == nil && shareID > 0 {
			line.ShareID = uintPointer(uint(shareID))
		}
		if fileID, err := strconv.ParseUint(req.PathValue("file_id"), 10, 64); err == nil && fileID > 0 {
			line.FileID = uintPointer(uint(fileID))
			if line.ShareID != nil {
				// Resolved before the handler runs, so a deleted file still has its path.
				line.Path = r.shareFilePath(*line.ShareID, uint(fileID))
			}
		}

		info := &shareAccessInfo{}
		req = req.WithContext(context.WithValue(req.Context(), shareAccessInfoKey{}, info))
		recorder := &shareAccessRecorder{ResponseWriter: w}
		next(recorder, req)

		status := recorder.statusCode()
		line.Success = status < http.StatusBadRequest
		line.UserID = info.userID
		if info.path != "" {
			line.Path = info.path
		}
		if line.Success {
			switch action {
			case model.ShareAccessLogActionRead:
				written := recorder.written
				line.Bytes = &written
			case model.ShareAccessLogActionUpload, model.ShareAccessLogActionUpdate:
				line.Bytes = info.bytes
			}
		} else {
			line.Message = recorder.errorMessage()
		}
		r.nodeLogs.RecordShareAccess(line)
	}
}

// recordFuseRead records one read of a file through a FUSE-mounted share.
func (r *Runtime) recordFuseRead(shareID, fileID uint, path string, bytes int64, err error) {
	if r == nil || r.nodeLogs == nil {
		return
	}
	line := service.ShareAccessLogLine{
		Timestamp: time.Now().UTC(),
		Action:    string(model.ShareAccessLogActionRead),
		Success:   err == nil,
		ShareID:   uintPointer(shareID),
		Path:      path,
		Source:    string(model.ShareAccessLogSourceFuse),
	}
	if fileID > 0 {
		line.FileID = uintPointer(fileID)
	}
	if err != nil {
		line.Message = err.Error()
	} else {
		line.Bytes = &bytes
	}
	r.nodeLogs.RecordShareAccess(line)
}

func (r *Runtime) shareByLinkHash(linkHash string) (apiclient.Share, bool) {
	if linkHash == "" {
		return apiclient.Share{}, false
	}
	shares, _ := r.shareStateSnapshot()
	for _, share := range shares {
		if share.LinkHash != nil && *share.LinkHash == linkHash {
			return share, true
		}
	}
	return apiclient.Share{}, false
}

func (r *Runtime) shareFilePath(shareID, fileID uint) string {
	shares, _ := r.shareStateSnapshot()
	for _, share := range shares {
		if share.ID != shareID {
			continue
		}
		r.stateMu.RLock()
		defer r.stateMu.RUnlock()
		for _, file := range r.replicaFiles[share.ReplicaID] {
			if file.FileID == fileID {
				return file.RelativeURI
			}
		}
		return ""
	}
	return ""
}

func uintPointer(value uint) *uint {
	return &value
}

// shareAccessRecorder captures the status, the body size and the start of error responses.
type shareAccessRecorder struct {
	http.ResponseWriter
	status   int
	written  int64
	errorBuf []byte
}

func (w *shareAccessRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *shareAccessRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	if w.status >= http.StatusBadRequest && len(w.errorBuf) < shareAccessErrorCaptureBytes {
		w.errorBuf = append(w.errorBuf, p[:min(n, shareAccessErrorCaptureBytes-len(w.errorBuf))]...)
	}
	return n, err
}

func (w *shareAccessRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *shareAccessRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *shareAccessRecorder) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// errorMessage returns the "error" field of a JSON error response, or the status text.
func (w *shareAccessRecorder) errorMessage() string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(w.errorBuf, &body) == nil && strings.TrimSpace(body.Error) != "" {
		return body.Error
	}
	return http.StatusText(w.statusCode())
}
