package metadata

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"path"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const sniffLength = 64

type format int

const (
	formatUnknown format = iota
	formatJPEG
	formatPNG
	formatGIF
	formatWebP
	formatBMP
	formatTIFF
	formatHEIF
	formatMP4
	formatMP3
	formatFLAC
	formatOgg
)

// Extract returns sanitized metadata for the content readable through r. Unsupported or malformed content yields
// no entries; extraction never fails the caller.
func Extract(name string, r io.ReaderAt, size int64) (entries []Entry) {
	if r == nil || size <= 0 {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("metadata extraction panic name=%s: %v", name, recovered)
			entries = nil
		}
	}()

	head := make([]byte, min(int64(sniffLength), size))
	n, _ := r.ReadAt(head, 0)
	head = head[:n]

	var result []Entry
	switch kind := detectFormat(name, head); kind {
	case formatJPEG:
		result = append(result, imageConfig(r, size)...)
		result = append(result, jpegEXIF(r, size)...)
	case formatPNG, formatGIF, formatWebP, formatBMP:
		result = append(result, imageConfig(r, size)...)
		result = append(result, embeddedEXIF(r, size)...)
	case formatTIFF:
		result = append(result, parseEXIF(r, 0, size)...)
		if ext := strings.ToLower(path.Ext(name)); ext == ".tif" || ext == ".tiff" {
			result = append(result, imageConfig(r, size)...)
		}
	case formatHEIF:
		result = append(result, embeddedEXIF(r, size)...)
	case formatMP4:
		mp4Entries, hasVideo := mp4Metadata(r, size)
		result = append(result, mp4Entries...)
		if !hasVideo {
			result = append(result, audioTags(r, size)...)
		}
	case formatMP3:
		result = append(result, audioTags(r, size)...)
		result = append(result, mp3Duration(r, size)...)
	case formatFLAC:
		result = append(result, audioTags(r, size)...)
		result = append(result, flacDuration(r, size)...)
	case formatOgg:
		result = append(result, audioTags(r, size)...)
	}
	return Sanitize(result)
}

func detectFormat(name string, head []byte) format {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return formatJPEG
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return formatPNG
	case bytes.HasPrefix(head, []byte("GIF8")):
		return formatGIF
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return formatWebP
	case bytes.HasPrefix(head, []byte("BM")) && strings.EqualFold(path.Ext(name), ".bmp"):
		return formatBMP
	case bytes.HasPrefix(head, []byte("II*\x00")), bytes.HasPrefix(head, []byte("MM\x00*")):
		return formatTIFF
	case bytes.HasPrefix(head, []byte("fLaC")):
		return formatFLAC
	case bytes.HasPrefix(head, []byte("OggS")):
		return formatOgg
	case bytes.HasPrefix(head, []byte("ID3")):
		return formatMP3
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		switch string(head[8:12]) {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1", "avif", "avis":
			return formatHEIF
		}
		return formatMP4
	case len(head) >= 8 && (string(head[4:8]) == "moov" || string(head[4:8]) == "mdat" || string(head[4:8]) == "wide" || string(head[4:8]) == "free"):
		return formatMP4
	}
	if strings.EqualFold(path.Ext(name), ".mp3") && len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 {
		return formatMP3
	}
	return formatUnknown
}

func imageConfig(r io.ReaderAt, size int64) []Entry {
	config, _, err := image.DecodeConfig(io.NewSectionReader(r, 0, size))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil
	}
	return []Entry{
		intEntry(SourceImage, KeyWidth, int64(config.Width)),
		intEntry(SourceImage, KeyHeight, int64(config.Height)),
	}
}
