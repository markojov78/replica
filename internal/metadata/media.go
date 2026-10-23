package metadata

import (
	"bytes"
	"encoding/binary"
	"io"
	"time"

	"github.com/abema/go-mp4"
	"github.com/dhowden/tag"
)

// mp4Epoch is the reference time of ISO base media file creation times.
var mp4Epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

// mp4Metadata reads duration, video dimensions and creation time from the moov box of MP4/MOV files. The second
// result reports whether the file has a video track.
func mp4Metadata(r io.ReaderAt, size int64) ([]Entry, bool) {
	boxes, err := mp4.ExtractBoxesWithPayload(io.NewSectionReader(r, 0, size), nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
	})
	if err != nil {
		return nil, false
	}

	var entries []Entry
	var width, height uint16
	for _, box := range boxes {
		switch payload := box.Payload.(type) {
		case *mp4.Mvhd:
			if payload.Timescale > 0 {
				durationMS := payload.GetDuration() * 1000 / uint64(payload.Timescale)
				if durationMS > 0 {
					entries = append(entries, intEntry(SourceMP4, KeyDurationMS, int64(durationMS)))
				}
			}
			if created := payload.GetCreationTime(); created > 0 {
				captured := mp4Epoch.Add(time.Duration(created) * time.Second)
				if captured.Year() >= 1971 && captured.Before(time.Now().Add(24*time.Hour)) {
					entries = append(entries,
						timeEntry(SourceMP4, KeyCaptureTime, captured),
						Entry{Source: SourceMP4, Key: KeyCaptureOffset, ValueType: ValueTypeText, Value: "+00:00"},
					)
				}
			}
		case *mp4.Tkhd:
			if w, h := payload.GetWidthInt(), payload.GetHeightInt(); w > width && h > 0 {
				width, height = w, h
			}
		}
	}
	if width > 0 && height > 0 {
		entries = append(entries, intEntry(SourceMP4, KeyWidth, int64(width)), intEntry(SourceMP4, KeyHeight, int64(height)))
	}
	return entries, width > 0
}

func audioTags(r io.ReaderAt, size int64) []Entry {
	tags, err := tag.ReadFrom(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil
	}
	var entries []Entry
	for _, field := range []struct{ key, value string }{
		{KeyArtist, tags.Artist()},
		{KeyAlbum, tags.Album()},
		{KeyTitle, tags.Title()},
	} {
		if entry, ok := textEntry(SourceTags, field.key, field.value); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

const mp3FrameSearchLimit = 64 * 1024

var (
	mp3SampleRates = [4][3]int{
		{11025, 12000, 8000},  // MPEG 2.5
		{},                    // reserved
		{22050, 24000, 16000}, // MPEG 2
		{44100, 48000, 32000}, // MPEG 1
	}
	mp3BitratesV1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3BitratesV2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
)

// mp3Duration calculates MPEG layer III duration from a Xing/Info or VBRI header, or from the bitrate of the first
// frame for constant bitrate files.
func mp3Duration(r io.ReaderAt, size int64) []Entry {
	start := int64(0)
	id3 := make([]byte, 10)
	if _, err := r.ReadAt(id3, 0); err == nil && bytes.HasPrefix(id3, []byte("ID3")) {
		tagSize := int64(id3[6]&0x7F)<<21 | int64(id3[7]&0x7F)<<14 | int64(id3[8]&0x7F)<<7 | int64(id3[9]&0x7F)
		start = 10 + tagSize
		if id3[5]&0x10 != 0 {
			start += 10
		}
	}

	buffer := make([]byte, min(mp3FrameSearchLimit, size-start))
	if len(buffer) < 4 {
		return nil
	}
	n, _ := r.ReadAt(buffer, start)
	buffer = buffer[:n]
	for i := 0; i+4 <= len(buffer); i++ {
		if buffer[i] != 0xFF || buffer[i+1]&0xE0 != 0xE0 {
			continue
		}
		version := (buffer[i+1] >> 3) & 0x03
		layer := (buffer[i+1] >> 1) & 0x03
		bitrateIndex := buffer[i+2] >> 4
		rateIndex := (buffer[i+2] >> 2) & 0x03
		if version == 1 || layer != 1 || bitrateIndex == 0 || bitrateIndex == 15 || rateIndex == 3 {
			continue
		}
		sampleRate := mp3SampleRates[version][rateIndex]
		mono := buffer[i+3]>>6 == 3
		samplesPerFrame := 576
		bitrate := mp3BitratesV2[bitrateIndex]
		sideInfo := 9
		if !mono {
			sideInfo = 17
		}
		if version == 3 {
			samplesPerFrame = 1152
			bitrate = mp3BitratesV1[bitrateIndex]
			sideInfo = 17
			if !mono {
				sideInfo = 32
			}
		}

		frame := buffer[i:]
		if frames, ok := mp3XingFrames(frame, 4+sideInfo); ok {
			return mp3DurationEntry(int64(frames) * int64(samplesPerFrame) * 1000 / int64(sampleRate))
		}
		if len(frame) >= 36+18 && string(frame[36:40]) == "VBRI" {
			frames := binary.BigEndian.Uint32(frame[36+14:])
			return mp3DurationEntry(int64(frames) * int64(samplesPerFrame) * 1000 / int64(sampleRate))
		}

		audioBytes := size - (start + int64(i))
		trailer := make([]byte, 3)
		if _, err := r.ReadAt(trailer, size-128); err == nil && string(trailer) == "TAG" {
			audioBytes -= 128
		}
		return mp3DurationEntry(audioBytes * 8 / int64(bitrate))
	}
	return nil
}

func mp3XingFrames(frame []byte, offset int) (uint32, bool) {
	if len(frame) < offset+12 {
		return 0, false
	}
	marker := string(frame[offset : offset+4])
	if marker != "Xing" && marker != "Info" {
		return 0, false
	}
	flags := binary.BigEndian.Uint32(frame[offset+4:])
	if flags&0x01 == 0 {
		return 0, false
	}
	frames := binary.BigEndian.Uint32(frame[offset+8:])
	return frames, frames > 0
}

func mp3DurationEntry(durationMS int64) []Entry {
	if durationMS <= 0 {
		return nil
	}
	return []Entry{intEntry(SourceMP3, KeyDurationMS, durationMS)}
}

// flacDuration reads the total sample count and sample rate from the FLAC STREAMINFO block.
func flacDuration(r io.ReaderAt, _ int64) []Entry {
	header := make([]byte, 8+34)
	if _, err := r.ReadAt(header, 0); err != nil || string(header[:4]) != "fLaC" || header[4]&0x7F != 0 {
		return nil
	}
	info := header[8:]
	sampleRate := int64(info[10])<<12 | int64(info[11])<<4 | int64(info[12]>>4)
	totalSamples := int64(info[13]&0x0F)<<32 | int64(binary.BigEndian.Uint32(info[14:]))
	if sampleRate == 0 || totalSamples == 0 {
		return nil
	}
	return []Entry{intEntry(SourceFLAC, KeyDurationMS, totalSamples*1000/sampleRate)}
}
