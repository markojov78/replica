package service

import (
	"net/url"
	"path"
	"strings"

	"replica/internal/model"
	"replica/internal/storageuri"
)

// replicaLocation identifies the physical storage behind a replica URI. Filesystem locations are scoped to their
// node; S3 and SFTP locations are global because different nodes can point at the same remote data.
type replicaLocation struct {
	scope      string
	components []string
}

func parseReplicaLocation(nodeID, uri string) (replicaLocation, bool) {
	trimmed := strings.TrimSpace(uri)
	switch {
	case strings.HasPrefix(trimmed, "s3://"):
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Host == "" {
			return replicaLocation{}, false
		}
		return replicaLocation{scope: "s3://" + parsed.Host, components: locationComponents(parsed.Path)}, true
	case strings.HasPrefix(trimmed, "sftp://"):
		parsed, err := storageuri.ParseSFTP(trimmed)
		if err != nil {
			return replicaLocation{}, false
		}
		return replicaLocation{scope: "sftp://" + parsed.Host, components: locationComponents(parsed.Path)}, true
	}

	filesystemPath := trimmed
	if strings.HasPrefix(trimmed, "file://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return replicaLocation{}, false
		}
		filesystemPath = parsed.Path
	}
	normalized := strings.ReplaceAll(filesystemPath, "\\", "/")
	root := ""
	switch {
	case len(normalized) >= 3 && normalized[0] == '/' && normalized[2] == ':':
		root = strings.ToUpper(normalized[1:3])
		normalized = normalized[3:]
	case len(normalized) >= 2 && normalized[1] == ':':
		root = strings.ToUpper(normalized[:2])
		normalized = normalized[2:]
	case strings.HasPrefix(normalized, "/"):
		root = "/"
	default:
		return replicaLocation{}, false
	}
	return replicaLocation{scope: "node:" + nodeID + ":" + root, components: locationComponents(normalized)}, true
}

func locationComponents(value string) []string {
	clean := path.Clean("/" + value)
	if clean == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(clean, "/"), "/")
}

// overlaps reports whether one location equals or contains the other.
func (l replicaLocation) overlaps(other replicaLocation) bool {
	if l.scope != other.scope {
		return false
	}
	for i := 0; i < len(l.components) && i < len(other.components); i++ {
		if l.components[i] != other.components[i] {
			return false
		}
	}
	return true
}

// overlappingReplicaError returns an ActiveReplicaLocationError when an active replica other than excludedID uses a
// location equal to, inside or containing the given one.
func overlappingReplicaError(activeReplicas []model.Replica, nodeID, uri string, excludedID uint) error {
	location, ok := parseReplicaLocation(nodeID, uri)
	if !ok {
		return nil
	}
	for _, replica := range activeReplicas {
		if replica.ID == excludedID {
			continue
		}
		other, ok := parseReplicaLocation(replica.NodeID, replica.URI)
		if ok && location.overlaps(other) {
			return &ActiveReplicaLocationError{ReplicaID: replica.ID, NodeID: replica.NodeID, URI: replica.URI}
		}
	}
	return nil
}
