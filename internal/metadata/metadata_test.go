package metadata

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"reflect"
	"testing"
	"time"
)

func extractStreamed(t *testing.T, name string, content []byte) map[string]string {
	t.Helper()
	capture := NewCapture()
	if _, err := io.Copy(capture, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	return Values(Extract(name, capture.ReaderAt(), capture.Size()))
}

func TestCaptureKeepsHeadAndTail(t *testing.T) {
	capture := NewCaptureWithLimits(4, 3)
	for _, chunk := range []string{"ab", "cdef", "ghij", "kl"} {
		if _, err := capture.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	reader := capture.ReaderAt()
	buffer := make([]byte, 4)
	if n, err := reader.ReadAt(buffer, 0); err != nil || string(buffer[:n]) != "abcd" {
		t.Fatalf("head = %q, %v", buffer[:n], err)
	}
	if n, err := reader.ReadAt(buffer[:3], 9); err != nil || string(buffer[:n]) != "jkl" {
		t.Fatalf("tail = %q, %v", buffer[:n], err)
	}
	if _, err := reader.ReadAt(buffer[:2], 5); !errors.Is(err, errRangeNotCaptured) {
		t.Fatalf("middle read error = %v", err)
	}
	if n, err := reader.ReadAt(buffer, 10); err != io.EOF || string(buffer[:n]) != "kl" {
		t.Fatalf("read past end = %q, %v", buffer[:n], err)
	}
}

func TestSanitizeDropsInvalidAndDuplicateEntries(t *testing.T) {
	entries := Sanitize([]Entry{
		{Source: SourceEXIF, Key: KeyWidth, ValueType: ValueTypeInt, Value: "100"},
		{Source: SourceImage, Key: KeyWidth, ValueType: ValueTypeInt, Value: "200"},
		{Source: SourceEXIF, Key: KeyHeight, ValueType: ValueTypeText, Value: "100"},
		{Source: SourceEXIF, Key: KeyCaptureTime, ValueType: ValueTypeTime, Value: "2024:01:01 10:00:00"},
		{Source: SourceEXIF, Key: "unknown", ValueType: ValueTypeText, Value: "x"},
		{Source: "", Key: KeyTitle, ValueType: ValueTypeText, Value: "x"},
		{Source: SourceTags, Key: KeyTitle, ValueType: ValueTypeText, Value: " Song "},
	})
	want := []Entry{
		{Source: SourceEXIF, Key: KeyWidth, ValueType: ValueTypeInt, Value: "100"},
		{Source: SourceTags, Key: KeyTitle, ValueType: ValueTypeText, Value: "Song"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("Sanitize() = %#v, want %#v", entries, want)
	}
}

func TestCaptureTimeAppliesOffset(t *testing.T) {
	captured, ok := CaptureTime(map[string]string{KeyCaptureTime: "2024-05-01T10:00:00", KeyCaptureOffset: "+02:00"})
	if !ok || !captured.Equal(time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("CaptureTime() = %v, %v", captured, ok)
	}
	captured, ok = CaptureTime(map[string]string{KeyCaptureTime: "2024-05-01T10:00:00"})
	if !ok || !captured.Equal(time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("CaptureTime() without offset = %v, %v", captured, ok)
	}
	if _, ok := CaptureTime(nil); ok {
		t.Fatal("CaptureTime(nil) ok")
	}
}

type tiffEntry struct {
	tag   uint16
	typ   uint16
	count uint32
	data  []byte
}

func asciiTag(tag uint16, value string) tiffEntry {
	return tiffEntry{tag: tag, typ: tiffTypeASCII, count: uint32(len(value) + 1), data: append([]byte(value), 0)}
}

func shortTag(tag uint16, value uint16) tiffEntry {
	data := make([]byte, 2)
	binary.LittleEndian.PutUint16(data, value)
	return tiffEntry{tag: tag, typ: tiffTypeShort, count: 1, data: data}
}

func longTag(tag uint16, value uint32) tiffEntry {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, value)
	return tiffEntry{tag: tag, typ: tiffTypeLong, count: 1, data: data}
}

func rationalTag(tag uint16, values ...[2]uint32) tiffEntry {
	data := make([]byte, 0, 8*len(values))
	for _, value := range values {
		data = binary.LittleEndian.AppendUint32(data, value[0])
		data = binary.LittleEndian.AppendUint32(data, value[1])
	}
	return tiffEntry{tag: tag, typ: tiffTypeRational, count: uint32(len(values)), data: data}
}

// buildTIFF lays out IFD0 followed by the EXIF and GPS IFDs, each IFD followed by its out-of-line values.
func buildTIFF(ifd0, exifIFD, gpsIFD []tiffEntry) []byte {
	ifdSize := func(entries []tiffEntry) int {
		size := 2 + 12*len(entries) + 4
		for _, entry := range entries {
			if len(entry.data) > 4 {
				size += len(entry.data)
			}
		}
		return size
	}
	ifd0Offset := 8
	ifd0 = append(ifd0, tiffEntry{}, tiffEntry{})
	exifOffset := ifd0Offset + ifdSize(ifd0)
	gpsOffset := exifOffset + ifdSize(exifIFD)
	ifd0[len(ifd0)-2] = longTag(tiffTagExifIFD, uint32(exifOffset))
	ifd0[len(ifd0)-1] = longTag(tiffTagGPSIFD, uint32(gpsOffset))

	out := []byte("II*\x00")
	out = binary.LittleEndian.AppendUint32(out, uint32(ifd0Offset))
	for _, ifd := range []struct {
		offset  int
		entries []tiffEntry
	}{{ifd0Offset, ifd0}, {exifOffset, exifIFD}, {gpsOffset, gpsIFD}} {
		valueOffset := ifd.offset + 2 + 12*len(ifd.entries) + 4
		var values []byte
		out = binary.LittleEndian.AppendUint16(out, uint16(len(ifd.entries)))
		for _, entry := range ifd.entries {
			out = binary.LittleEndian.AppendUint16(out, entry.tag)
			out = binary.LittleEndian.AppendUint16(out, entry.typ)
			out = binary.LittleEndian.AppendUint32(out, entry.count)
			if len(entry.data) > 4 {
				out = binary.LittleEndian.AppendUint32(out, uint32(valueOffset+len(values)))
				values = append(values, entry.data...)
			} else {
				out = append(out, append(entry.data, make([]byte, 4-len(entry.data))...)...)
			}
		}
		out = binary.LittleEndian.AppendUint32(out, 0)
		out = append(out, values...)
	}
	return out
}

func testEXIF() []byte {
	return buildTIFF(
		[]tiffEntry{asciiTag(tiffTagMake, "Canon"), asciiTag(tiffTagModel, "EOS R6"), shortTag(tiffTagOrientation, 6)},
		[]tiffEntry{
			asciiTag(exifTagDateTimeOriginal, "2024:05:01 10:20:30"),
			asciiTag(exifTagOffsetTimeOriginal, "+02:00"),
			longTag(exifTagPixelXDimension, 6000),
			longTag(exifTagPixelYDimension, 4000),
		},
		[]tiffEntry{
			asciiTag(gpsTagLatitudeRef, "N"),
			rationalTag(gpsTagLatitude, [2]uint32{44, 1}, [2]uint32{48, 1}, [2]uint32{36, 1}),
			asciiTag(gpsTagLongitudeRef, "W"),
			rationalTag(gpsTagLongitude, [2]uint32{20, 1}, [2]uint32{27, 1}, [2]uint32{0, 1}),
		},
	)
}

func TestExtractJPEGEXIF(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, 32, 16)), nil); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte(nil), exifHeader...)
	payload = append(payload, testEXIF()...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(payload)+2))
	content := append([]byte{0xFF, 0xD8}, app1...)
	content = append(content, payload...)
	content = append(content, encoded.Bytes()[2:]...)

	got := extractStreamed(t, "photo.jpg", content)
	want := map[string]string{
		KeyCaptureTime:   "2024-05-01T10:20:30",
		KeyCaptureOffset: "+02:00",
		KeyWidth:         "32",
		KeyHeight:        "16",
		KeyOrientation:   "6",
		KeyCameraMake:    "Canon",
		KeyCameraModel:   "EOS R6",
		KeyGPSLat:        "44.81",
		KeyGPSLon:        "-20.45",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract() = %#v, want %#v", got, want)
	}
}

func TestExtractEmbeddedEXIF(t *testing.T) {
	content := []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic")
	content = append(content, make([]byte, 100)...)
	content = append(content, 0, 0, 0, 6)
	content = append(content, exifHeader...)
	content = append(content, testEXIF()...)

	got := extractStreamed(t, "photo.heic", content)
	if got[KeyCaptureTime] != "2024-05-01T10:20:30" || got[KeyWidth] != "6000" || got[KeyCameraModel] != "EOS R6" {
		t.Fatalf("Extract() = %#v", got)
	}
}

func mp4Box(boxType string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	out = append(out, boxType...)
	return append(out, body...)
}

func TestExtractMP4WithMoovAfterLargeMdat(t *testing.T) {
	created := uint32(time.Date(2023, 7, 1, 12, 0, 0, 0, time.UTC).Sub(mp4Epoch) / time.Second)
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[4:], created)
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], 2500)
	binary.BigEndian.PutUint32(mvhd[20:], 0x00010000)
	binary.BigEndian.PutUint16(mvhd[24:], 0x0100)
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:], 1920<<16)
	binary.BigEndian.PutUint32(tkhd[80:], 1080<<16)

	content := mp4Box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	content = append(content, mp4Box("mdat", make([]byte, 3<<20))...)
	content = append(content, mp4Box("moov", mp4Box("mvhd", mvhd), mp4Box("trak", mp4Box("tkhd", tkhd)))...)

	got := extractStreamed(t, "clip.mp4", content)
	want := map[string]string{
		KeyDurationMS:    "2500",
		KeyCaptureTime:   "2023-07-01T12:00:00",
		KeyCaptureOffset: "+00:00",
		KeyWidth:         "1920",
		KeyHeight:        "1080",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract() = %#v, want %#v", got, want)
	}
}

func TestExtractMP3Duration(t *testing.T) {
	id3 := []byte("ID3\x04\x00\x00\x00\x00\x00\x00")
	frame := []byte{0xFF, 0xFB, 0x90, 0x00}

	xing := append(append([]byte(nil), frame...), make([]byte, 32)...)
	xing = append(xing, "Xing"...)
	xing = binary.BigEndian.AppendUint32(xing, 1)
	xing = binary.BigEndian.AppendUint32(xing, 100)
	xing = append(xing, make([]byte, 400)...)
	if got := extractStreamed(t, "song.mp3", append(id3, xing...)); got[KeyDurationMS] != "2612" {
		t.Fatalf("Xing duration = %#v", got)
	}

	cbr := append(append([]byte(nil), frame...), make([]byte, 16000-len(frame))...)
	if got := extractStreamed(t, "song.mp3", append(id3, cbr...)); got[KeyDurationMS] != "1000" {
		t.Fatalf("CBR duration = %#v", got)
	}
}

func TestExtractFLACDuration(t *testing.T) {
	const sampleRate, channels, bits, total = 44100, 2, 16, 441000
	info := make([]byte, 34)
	info[10] = byte(sampleRate >> 12)
	info[11] = byte(sampleRate >> 4 & 0xFF)
	info[12] = byte(sampleRate&0x0F)<<4 | byte(channels-1)<<1 | byte(bits-1)>>4
	info[13] = byte(bits-1)&0x0F<<4 | byte(total>>32)&0x0F
	binary.BigEndian.PutUint32(info[14:], uint32(total))
	content := append([]byte("fLaC\x80\x00\x00\x22"), info...)

	if got := extractStreamed(t, "song.flac", content); got[KeyDurationMS] != "10000" {
		t.Fatalf("Extract() = %#v", got)
	}
}

func TestExtractIgnoresUnknownAndMalformedContent(t *testing.T) {
	for name, content := range map[string][]byte{
		"notes.txt":  []byte("plain text"),
		"broken.jpg": {0xFF, 0xD8, 0xFF, 0xE1, 0x00},
		"broken.mp4": []byte("\x00\x00\x00\x10ftypisom\xFF\xFF\xFF\xFFmoov"),
	} {
		if got := extractStreamed(t, name, content); got != nil {
			t.Fatalf("Extract(%s) = %#v", name, got)
		}
	}
}
