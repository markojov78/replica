package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"

	"replica/internal/apiclient"
	"replica/internal/service"
)

func (r *Runtime) serveShareFilePreview(w http.ResponseWriter, req *http.Request, share apiclient.Share, replica apiclient.Replica, fileID uint) {
	if err := validateShareRangeHeader(req.Header.Get("Range")); err != nil {
		writeStorageShareError(w, http.StatusBadRequest, err.Error())
		return
	}
	file, err := r.shareFileForReadInShare(share, replica.ID, fileID)
	if err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	r.stateMu.RLock()
	preview := r.progressiveJPEG
	r.stateMu.RUnlock()
	if preview == nil {
		writeStorageShareError(w, http.StatusInternalServerError, "preview service unavailable")
		return
	}
	open := func(ctx context.Context) (io.ReadCloser, int64, error) {
		profile, err := r.GetPprofile(replica.StorageProfile)
		if err != nil {
			return nil, 0, err
		}
		reader, err := GetReader(ctx, replica.URI, profile)
		if err != nil {
			return nil, 0, err
		}
		return reader.Open(ctx, replica.URI, file.RelativeURI)
	}
	result, err := preview.GetOrCreate(req.Context(), service.ProgressiveJPEGRequest{
		FileID: file.FileID, FileVersion: file.InventoryVersion,
		RelativeURI: file.RelativeURI, Size: file.Size,
		Open: func(ctx context.Context) (io.ReadCloser, error) {
			content, _, err := open(ctx)
			return content, err
		},
	})
	if err != nil {
		writeStorageShareError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer result.Release()

	var content io.Reader
	var size int64
	representation := "original"
	if result.Path != "" {
		generated, err := os.Open(result.Path)
		if err != nil {
			writeStorageShareError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer generated.Close()
		content = generated
		representation = "preview"
	} else if result.Original != nil {
		content = bytes.NewReader(result.Original)
		size = int64(len(result.Original))
	} else {
		original, originalSize, err := open(req.Context())
		if err != nil {
			writeStorageShareError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer original.Close()
		content, size = original, originalSize
	}

	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Content-Disposition", shareContentDisposition(file.RelativeURI, "inline"))
	w.Header().Set("ETag", fmt.Sprintf(`"file-%d-v%d-%s"`, file.FileID, file.InventoryVersion, representation))
	if representation == "preview" {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	if seeker, ok := content.(io.ReadSeeker); ok {
		http.ServeContent(w, req, path.Base(file.RelativeURI), file.Modified, seeker)
		return
	}
	w.Header().Set("Content-Type", contentTypeByName(file.RelativeURI))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, content)
}
