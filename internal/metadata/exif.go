package metadata

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"time"
)

const (
	tiffTagMake                = 0x010F
	tiffTagModel               = 0x0110
	tiffTagOrientation         = 0x0112
	tiffTagExifIFD             = 0x8769
	tiffTagGPSIFD              = 0x8825
	exifTagDateTimeOriginal    = 0x9003
	exifTagDateTimeDigitized   = 0x9004
	exifTagOffsetTimeOriginal  = 0x9011
	exifTagOffsetTimeDigitized = 0x9012
	exifTagPixelXDimension     = 0xA002
	exifTagPixelYDimension     = 0xA003
	gpsTagLatitudeRef          = 0x0001
	gpsTagLatitude             = 0x0002
	gpsTagLongitudeRef         = 0x0003
	gpsTagLongitude            = 0x0004

	tiffTypeASCII    = 2
	tiffTypeShort    = 3
	tiffTypeLong     = 4
	tiffTypeRational = 5

	maxIFDEntries   = 1024
	maxTagValueSize = 4096
	exifSearchLimit = 1 << 20
)

var exifHeader = []byte("Exif\x00\x00")

type tiffTag struct {
	typ    uint16
	count  uint32
	data   []byte
	offset bool
}

type tiffReader struct {
	r     io.ReaderAt
	base  int64
	limit int64
	order binary.ByteOrder
}

// jpegEXIF reads the EXIF APP1 segment of a JPEG stream.
func jpegEXIF(r io.ReaderAt, size int64) []Entry {
	offset := int64(2)
	marker := make([]byte, 4)
	for offset+4 <= size {
		if _, err := r.ReadAt(marker, offset); err != nil {
			return nil
		}
		if marker[0] != 0xFF {
			return nil
		}
		code := marker[1]
		if code == 0xD8 || (code >= 0xD0 && code <= 0xD7) || code == 0x01 || code == 0xFF {
			offset += 2
			continue
		}
		if code == 0xDA || code == 0xD9 {
			return nil
		}
		length := int64(binary.BigEndian.Uint16(marker[2:]))
		if length < 2 {
			return nil
		}
		if code == 0xE1 && length >= 8 {
			header := make([]byte, len(exifHeader))
			if _, err := r.ReadAt(header, offset+4); err == nil && bytes.Equal(header, exifHeader) {
				return parseEXIF(r, offset+4+int64(len(exifHeader)), min(size, offset+2+length))
			}
		}
		offset += 2 + length
	}
	return nil
}

// embeddedEXIF finds an "Exif\0\0" block followed by a TIFF header in the beginning of a container such as HEIF,
// WebP or PNG.
func embeddedEXIF(r io.ReaderAt, size int64) []Entry {
	buffer := make([]byte, min(size, exifSearchLimit))
	n, _ := r.ReadAt(buffer, 0)
	buffer = buffer[:n]
	for start := 0; start < len(buffer); {
		index := bytes.Index(buffer[start:], exifHeader)
		if index < 0 {
			break
		}
		tiffStart := start + index + len(exifHeader)
		if tiffStart+4 <= len(buffer) && isTIFFHeader(buffer[tiffStart:tiffStart+4]) {
			return parseEXIF(r, int64(tiffStart), size)
		}
		start = tiffStart
	}
	if bytes.HasPrefix(buffer, []byte("\x89PNG\r\n\x1a\n")) {
		if offset, ok := pngEXIFChunk(buffer); ok {
			return parseEXIF(r, offset, size)
		}
	}
	return nil
}

func pngEXIFChunk(buffer []byte) (int64, bool) {
	offset := 8
	for offset+8 <= len(buffer) {
		length := int(binary.BigEndian.Uint32(buffer[offset:]))
		chunkType := string(buffer[offset+4 : offset+8])
		if chunkType == "eXIf" {
			return int64(offset + 8), true
		}
		if chunkType == "IDAT" || chunkType == "IEND" {
			return 0, false
		}
		offset += 12 + length
	}
	return 0, false
}

func isTIFFHeader(header []byte) bool {
	return bytes.Equal(header, []byte("II*\x00")) || bytes.Equal(header, []byte("MM\x00*"))
}

// parseEXIF parses TIFF structured EXIF data that starts at base.
func parseEXIF(r io.ReaderAt, base, limit int64) []Entry {
	header := make([]byte, 8)
	if _, err := r.ReadAt(header, base); err != nil || !isTIFFHeader(header[:4]) {
		return nil
	}
	t := &tiffReader{r: r, base: base, limit: limit, order: binary.LittleEndian}
	if header[0] == 'M' {
		t.order = binary.BigEndian
	}

	ifd0, err := t.readIFD(int64(t.order.Uint32(header[4:])))
	if err != nil {
		return nil
	}
	var exifIFD, gpsIFD map[uint16]tiffTag
	if offset, ok := t.uintValue(ifd0[tiffTagExifIFD]); ok {
		exifIFD, _ = t.readIFD(int64(offset))
	}
	if offset, ok := t.uintValue(ifd0[tiffTagGPSIFD]); ok {
		gpsIFD, _ = t.readIFD(int64(offset))
	}

	var entries []Entry
	captured, offset := t.captureTime(exifIFD, exifTagDateTimeOriginal, exifTagOffsetTimeOriginal)
	if captured.IsZero() {
		captured, offset = t.captureTime(exifIFD, exifTagDateTimeDigitized, exifTagOffsetTimeDigitized)
	}
	if !captured.IsZero() {
		entries = append(entries, timeEntry(SourceEXIF, KeyCaptureTime, captured))
		if offset != "" {
			entries = append(entries, Entry{Source: SourceEXIF, Key: KeyCaptureOffset, ValueType: ValueTypeText, Value: offset})
		}
	}
	if width, ok := t.uintValue(exifIFD[exifTagPixelXDimension]); ok && width > 0 {
		if height, ok := t.uintValue(exifIFD[exifTagPixelYDimension]); ok && height > 0 {
			entries = append(entries, intEntry(SourceEXIF, KeyWidth, int64(width)), intEntry(SourceEXIF, KeyHeight, int64(height)))
		}
	}
	if orientation, ok := t.uintValue(ifd0[tiffTagOrientation]); ok && orientation >= 1 && orientation <= 8 {
		entries = append(entries, intEntry(SourceEXIF, KeyOrientation, int64(orientation)))
	}
	if entry, ok := textEntry(SourceEXIF, KeyCameraMake, t.stringValue(ifd0[tiffTagMake])); ok {
		entries = append(entries, entry)
	}
	if entry, ok := textEntry(SourceEXIF, KeyCameraModel, t.stringValue(ifd0[tiffTagModel])); ok {
		entries = append(entries, entry)
	}
	if lat, lon, ok := t.gpsPosition(gpsIFD); ok {
		entries = append(entries, floatEntry(SourceEXIF, KeyGPSLat, lat), floatEntry(SourceEXIF, KeyGPSLon, lon))
	}
	return entries
}

func (t *tiffReader) readIFD(offset int64) (map[uint16]tiffTag, error) {
	countBytes := make([]byte, 2)
	if err := t.read(countBytes, offset); err != nil {
		return nil, err
	}
	count := int(t.order.Uint16(countBytes))
	if count > maxIFDEntries {
		return nil, io.ErrUnexpectedEOF
	}
	raw := make([]byte, count*12)
	if err := t.read(raw, offset+2); err != nil {
		return nil, err
	}
	tags := make(map[uint16]tiffTag, count)
	for i := 0; i < count; i++ {
		entry := raw[i*12 : i*12+12]
		tag := t.order.Uint16(entry[0:])
		typ := t.order.Uint16(entry[2:])
		valueCount := t.order.Uint32(entry[4:])
		valueSize := tiffTypeSize(typ) * int64(valueCount)
		if valueSize <= 0 || valueSize > maxTagValueSize {
			continue
		}
		data := entry[8:12]
		if valueSize > 4 {
			data = make([]byte, valueSize)
			if err := t.read(data, int64(t.order.Uint32(entry[8:]))); err != nil {
				continue
			}
		} else {
			data = append([]byte(nil), data[:valueSize]...)
		}
		tags[tag] = tiffTag{typ: typ, count: valueCount, data: data}
	}
	return tags, nil
}

func (t *tiffReader) read(p []byte, offset int64) error {
	if offset < 0 || t.base+offset+int64(len(p)) > t.limit {
		return io.ErrUnexpectedEOF
	}
	_, err := t.r.ReadAt(p, t.base+offset)
	return err
}

func tiffTypeSize(typ uint16) int64 {
	switch typ {
	case 1, tiffTypeASCII, 6, 7:
		return 1
	case tiffTypeShort, 8:
		return 2
	case tiffTypeLong, 9, 11:
		return 4
	case tiffTypeRational, 10, 12:
		return 8
	}
	return 0
}

func (t *tiffReader) uintValue(tag tiffTag) (uint32, bool) {
	switch {
	case tag.typ == tiffTypeShort && len(tag.data) >= 2:
		return uint32(t.order.Uint16(tag.data)), true
	case tag.typ == tiffTypeLong && len(tag.data) >= 4:
		return t.order.Uint32(tag.data), true
	}
	return 0, false
}

func (t *tiffReader) stringValue(tag tiffTag) string {
	if tag.typ != tiffTypeASCII {
		return ""
	}
	if index := bytes.IndexByte(tag.data, 0); index >= 0 {
		return string(tag.data[:index])
	}
	return string(tag.data)
}

func (t *tiffReader) rationals(tag tiffTag) []float64 {
	if tag.typ != tiffTypeRational {
		return nil
	}
	values := make([]float64, 0, tag.count)
	for i := 0; i+8 <= len(tag.data); i += 8 {
		numerator := t.order.Uint32(tag.data[i:])
		denominator := t.order.Uint32(tag.data[i+4:])
		if denominator == 0 {
			return nil
		}
		values = append(values, float64(numerator)/float64(denominator))
	}
	return values
}

func (t *tiffReader) captureTime(tags map[uint16]tiffTag, timeTag, offsetTag uint16) (time.Time, string) {
	value := t.stringValue(tags[timeTag])
	captured, err := time.Parse("2006:01:02 15:04:05", value)
	if err != nil || captured.Year() < 1800 {
		return time.Time{}, ""
	}
	offset := t.stringValue(tags[offsetTag])
	if _, ok := parseOffset(offset); !ok {
		offset = ""
	}
	return captured, offset
}

func (t *tiffReader) gpsPosition(tags map[uint16]tiffTag) (float64, float64, bool) {
	lat, ok := gpsCoordinate(t.rationals(tags[gpsTagLatitude]), t.stringValue(tags[gpsTagLatitudeRef]), "S", 90)
	if !ok {
		return 0, 0, false
	}
	lon, ok := gpsCoordinate(t.rationals(tags[gpsTagLongitude]), t.stringValue(tags[gpsTagLongitudeRef]), "W", 180)
	if !ok {
		return 0, 0, false
	}
	return lat, lon, true
}

func gpsCoordinate(parts []float64, ref, negativeRef string, limit float64) (float64, bool) {
	if len(parts) != 3 || ref == "" {
		return 0, false
	}
	value := parts[0] + parts[1]/60 + parts[2]/3600
	if value > limit || math.IsNaN(value) {
		return 0, false
	}
	if ref == negativeRef {
		value = -value
	}
	return math.Round(value*1e7) / 1e7, true
}
