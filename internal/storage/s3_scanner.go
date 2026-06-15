package storage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type s3ListObjectsV2API interface {
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type s3HeadObjectAPI interface {
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

type S3Scanner struct {
	listClient s3ListObjectsV2API
	headClient s3HeadObjectAPI
	getClient  s3GetObjectAPI

	cacheMu sync.RWMutex
	cache   map[string]s3CachedHash
}

type s3CachedHash struct {
	fingerprint string
	hash        string
}

func NewS3Scanner(client *s3.Client) *S3Scanner {
	return &S3Scanner{
		listClient: client,
		headClient: client,
		getClient:  client,
		cache:      make(map[string]s3CachedHash),
	}
}

func NewS3ScannerWithClients(listClient s3ListObjectsV2API, headClient s3HeadObjectAPI) *S3Scanner {
	getClient, _ := listClient.(s3GetObjectAPI)
	return NewS3ScannerWithAllClients(listClient, headClient, getClient)
}

func NewS3ScannerWithAllClients(listClient s3ListObjectsV2API, headClient s3HeadObjectAPI, getClient s3GetObjectAPI) *S3Scanner {
	return &S3Scanner{
		listClient: listClient,
		headClient: headClient,
		getClient:  getClient,
		cache:      make(map[string]s3CachedHash),
	}
}

func (s *S3Scanner) Scan(ctx context.Context, rootURI string, oldStates map[string]FileState, targetRelativeURI ...string) ([]FileState, error) {
	location, err := parseS3URI(rootURI)
	if err != nil {
		return nil, err
	}
	targets, err := cleanRelativeURISet(targetRelativeURI)
	if err != nil {
		return nil, err
	}

	states := make([]FileState, 0)
	var continuation *string
	for {
		output, err := s.listClient.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(location.Bucket),
			Prefix:            aws.String(location.ListPrefix()),
			ContinuationToken: continuation,
		})
		if err != nil {
			return nil, err
		}

		for _, object := range output.Contents {
			if len(targets) > 0 {
				relativeURI, ok := location.RelativeKey(aws.ToString(object.Key))
				if !ok {
					continue
				}
				if _, ok := targets[normalizeRelativeURI(relativeURI)]; !ok {
					continue
				}
			}
			state, err := s.fileStateFromObject(ctx, location, object, oldStates)
			if err != nil {
				return nil, err
			}
			if state == nil {
				continue
			}
			states = append(states, *state)
		}

		if !aws.ToBool(output.IsTruncated) || output.NextContinuationToken == nil {
			break
		}
		continuation = output.NextContinuationToken
	}

	sortFileStates(states)
	return states, nil
}

func (s *S3Scanner) fileStateFromObject(ctx context.Context, location s3Location, object types.Object, oldStates map[string]FileState) (*FileState, error) {
	key := aws.ToString(object.Key)
	if key == "" {
		return nil, nil
	}

	relativeURI, ok := location.RelativeKey(key)
	if !ok || relativeURI == "" {
		return nil, nil
	}
	if isTemporaryWritePath(relativeURI) {
		return nil, nil
	}

	normalizedRelativeURI := normalizeRelativeURI(relativeURI)
	size := aws.ToInt64(object.Size)
	modified := time.Time{}
	if object.LastModified != nil {
		modified = object.LastModified.UTC()
	}

	if oldState, ok := oldStateWithMatchingMetadata(oldStates, normalizedRelativeURI, size, modified); ok {
		return &FileState{
			RelativeURI: normalizedRelativeURI,
			Size:        size,
			Hash:        oldState.Hash,
			Created:     modified,
			Modified:    modified,
		}, nil
	}

	fingerprint := s3ObjectFingerprint(object)
	if s.headClient != nil {
		headOutput, err := s.headClient.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket:       aws.String(location.Bucket),
			Key:          aws.String(key),
			ChecksumMode: types.ChecksumModeEnabled,
		})
		if err != nil {
			return nil, err
		}
		if checksumHash, checksumAlgorithm := s3ChecksumFromHeadObject(headOutput); checksumHash != "" {
			fingerprint += "|" + checksumAlgorithm + ":" + checksumHash
		}
	}

	cacheKey := location.Bucket + "/" + key
	hash, ok := s.cachedHash(cacheKey, fingerprint)
	if !ok {
		if s.getClient == nil {
			return nil, errors.New("s3 scanner requires object read access to calculate BLAKE3")
		}
		output, err := s.getClient.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(location.Bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return nil, err
		}
		hash, err = hashReaderBLAKE3(ctx, output.Body)
		closeErr := output.Body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		s.storeCachedHash(cacheKey, fingerprint, hash)
	}

	return &FileState{
		RelativeURI: normalizedRelativeURI,
		Size:        size,
		Hash:        hash,
		Created:     modified,
		Modified:    modified,
	}, nil
}

func (s *S3Scanner) cachedHash(key, fingerprint string) (string, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	cached, ok := s.cache[key]
	return cached.hash, ok && cached.fingerprint == fingerprint
}

func (s *S3Scanner) storeCachedHash(key, fingerprint, hash string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cache[key] = s3CachedHash{fingerprint: fingerprint, hash: hash}
}

func s3ObjectFingerprint(object types.Object) string {
	modified := ""
	if object.LastModified != nil {
		modified = object.LastModified.UTC().Format(time.RFC3339Nano)
	}
	etag := s3ETagFingerprint(object.ETag)
	return fmt.Sprintf("%d|%s|%s", aws.ToInt64(object.Size), modified, etag)
}

type s3Location struct {
	Bucket string
	Prefix string
}

func (l s3Location) ListPrefix() string {
	if l.Prefix == "" {
		return ""
	}
	return l.Prefix + "/"
}

func (l s3Location) RelativeKey(key string) (string, bool) {
	if l.Prefix == "" {
		return strings.TrimPrefix(key, "/"), true
	}
	prefix := l.Prefix + "/"
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	return strings.TrimPrefix(key, prefix), true
}

func parseS3URI(rawURI string) (s3Location, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return s3Location{}, err
	}
	if parsed.Scheme != "s3" {
		return s3Location{}, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return s3Location{}, errors.New("s3 URI bucket is required")
	}

	prefix := strings.Trim(path.Clean(strings.TrimPrefix(parsed.Path, "/")), ".")
	if prefix == "/" {
		prefix = ""
	}

	return s3Location{
		Bucket: parsed.Host,
		Prefix: strings.Trim(prefix, "/"),
	}, nil
}

func s3ETagFingerprint(etag *string) string {
	if etag == nil {
		return ""
	}
	return strings.Trim(*etag, `"`)
}

func s3ChecksumFromHeadObject(output *s3.HeadObjectOutput) (string, string) {
	switch {
	case output == nil:
		return "", ""
	case output.ChecksumSHA256 != nil:
		return aws.ToString(output.ChecksumSHA256), "sha256"
	case output.ChecksumSHA1 != nil:
		return aws.ToString(output.ChecksumSHA1), "sha1"
	case output.ChecksumCRC32C != nil:
		return aws.ToString(output.ChecksumCRC32C), "crc32c"
	case output.ChecksumCRC32 != nil:
		return aws.ToString(output.ChecksumCRC32), "crc32"
	case output.ChecksumCRC64NVME != nil:
		return aws.ToString(output.ChecksumCRC64NVME), "crc64nvme"
	default:
		return "", ""
	}
}
