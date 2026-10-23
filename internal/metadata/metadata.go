// Package metadata extracts file-type specific metadata (camera capture time, image dimensions, media duration,
// audio tags) used by the share viewer. Metadata never participates in file identity or replication.
package metadata

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ValueTypeText  = "text"
	ValueTypeInt   = "int"
	ValueTypeFloat = "float"
	ValueTypeTime  = "time"
	ValueTypeBool  = "bool"
)

// TimeLayout is the canonical format of time values. Times carry no zone; a known offset is stored in a separate key.
const TimeLayout = "2006-01-02T15:04:05"

const (
	KeyCaptureTime   = "capture_time"
	KeyCaptureOffset = "capture_offset"
	KeyWidth         = "width"
	KeyHeight        = "height"
	KeyOrientation   = "orientation"
	KeyCameraMake    = "camera_make"
	KeyCameraModel   = "camera_model"
	KeyGPSLat        = "gps_lat"
	KeyGPSLon        = "gps_lon"
	KeyDurationMS    = "duration_ms"
	KeyArtist        = "artist"
	KeyAlbum         = "album"
	KeyTitle         = "title"
)

const (
	SourceEXIF  = "exif"
	SourceImage = "image"
	SourceMP4   = "mp4"
	SourceMP3   = "mp3"
	SourceFLAC  = "flac"
	SourceTags  = "tags"
)

const (
	MaxEntries      = 32
	MaxTextLength   = 512
	MaxSourceLength = 32
)

// keyTypes is the whitelist of canonical keys; each key always has the same value type.
var keyTypes = map[string]string{
	KeyCaptureTime:   ValueTypeTime,
	KeyCaptureOffset: ValueTypeText,
	KeyWidth:         ValueTypeInt,
	KeyHeight:        ValueTypeInt,
	KeyOrientation:   ValueTypeInt,
	KeyCameraMake:    ValueTypeText,
	KeyCameraModel:   ValueTypeText,
	KeyGPSLat:        ValueTypeFloat,
	KeyGPSLon:        ValueTypeFloat,
	KeyDurationMS:    ValueTypeInt,
	KeyArtist:        ValueTypeText,
	KeyAlbum:         ValueTypeText,
	KeyTitle:         ValueTypeText,
}

type Entry struct {
	Source    string `json:"source"`
	Key       string `json:"key"`
	ValueType string `json:"value_type"`
	Value     string `json:"value"`
}

// KeyValueType returns the value type of a canonical key.
func KeyValueType(key string) (string, bool) {
	valueType, ok := keyTypes[key]
	return valueType, ok
}

// Valid reports whether an entry uses a known key with its value type and a value in canonical format.
func Valid(entry Entry) bool {
	if entry.Source == "" || len(entry.Source) > MaxSourceLength {
		return false
	}
	valueType, ok := keyTypes[entry.Key]
	if !ok || valueType != entry.ValueType {
		return false
	}
	switch valueType {
	case ValueTypeText:
		return entry.Value != "" && len(entry.Value) <= MaxTextLength && utf8.ValidString(entry.Value)
	case ValueTypeInt:
		_, err := strconv.ParseInt(entry.Value, 10, 64)
		return err == nil
	case ValueTypeFloat:
		_, err := strconv.ParseFloat(entry.Value, 64)
		return err == nil
	case ValueTypeTime:
		_, err := time.Parse(TimeLayout, entry.Value)
		return err == nil
	case ValueTypeBool:
		return entry.Value == "true" || entry.Value == "false"
	}
	return false
}

// Sanitize drops invalid entries, keeps the first entry per key and caps the entry count.
func Sanitize(entries []Entry) []Entry {
	if len(entries) == 0 {
		return nil
	}
	result := make([]Entry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry.Source = strings.TrimSpace(entry.Source)
		entry.Key = strings.TrimSpace(entry.Key)
		entry.Value = strings.TrimSpace(entry.Value)
		if !Valid(entry) {
			continue
		}
		if _, ok := seen[entry.Key]; ok {
			continue
		}
		seen[entry.Key] = struct{}{}
		result = append(result, entry)
		if len(result) == MaxEntries {
			break
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// Values converts entries to the key/value map exposed to viewers.
func Values(entries []Entry) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		values[entry.Key] = entry.Value
	}
	return values
}

// CaptureTime returns the capture instant from viewer metadata values. A capture time without a known offset is
// treated as UTC.
func CaptureTime(values map[string]string) (time.Time, bool) {
	value, ok := values[KeyCaptureTime]
	if !ok {
		return time.Time{}, false
	}
	captured, err := time.Parse(TimeLayout, value)
	if err != nil {
		return time.Time{}, false
	}
	if offset, ok := parseOffset(values[KeyCaptureOffset]); ok {
		captured = captured.Add(-offset)
	}
	return captured, true
}

// Int returns an int metadata value.
func Int(values map[string]string, key string) (int64, bool) {
	value, ok := values[key]
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil
}

// parseOffset parses an EXIF style offset such as "+02:00" or "-05:30".
func parseOffset(value string) (time.Duration, bool) {
	if len(value) != 6 || (value[0] != '+' && value[0] != '-') || value[3] != ':' {
		return 0, false
	}
	hours, err := strconv.Atoi(value[1:3])
	if err != nil {
		return 0, false
	}
	minutes, err := strconv.Atoi(value[4:6])
	if err != nil || hours > 14 || minutes > 59 {
		return 0, false
	}
	offset := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	if value[0] == '-' {
		offset = -offset
	}
	return offset, true
}

func textEntry(source, key, value string) (Entry, bool) {
	value = strings.TrimSpace(strings.TrimRight(value, "\x00"))
	if value == "" {
		return Entry{}, false
	}
	if len(value) > MaxTextLength {
		value = value[:MaxTextLength]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
	}
	return Entry{Source: source, Key: key, ValueType: ValueTypeText, Value: value}, true
}

func intEntry(source, key string, value int64) Entry {
	return Entry{Source: source, Key: key, ValueType: ValueTypeInt, Value: strconv.FormatInt(value, 10)}
}

func floatEntry(source, key string, value float64) Entry {
	return Entry{Source: source, Key: key, ValueType: ValueTypeFloat, Value: strconv.FormatFloat(value, 'f', -1, 64)}
}

func timeEntry(source, key string, value time.Time) Entry {
	return Entry{Source: source, Key: key, ValueType: ValueTypeTime, Value: value.Format(TimeLayout)}
}
