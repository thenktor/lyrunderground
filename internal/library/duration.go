package library

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/abema/go-mp4"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
	"github.com/tcolgate/mp3"
)

func durationFor(f *os.File, ext string) (time.Duration, error) {
	switch ext {
	case ".mp3":
		return mp3Duration(f)
	case ".ogg", ".opus":
		return oggDuration(f)
	case ".flac":
		return flacDuration(f)
	case ".m4a":
		return m4aDuration(f)
	default:
		return 0, fmt.Errorf("nicht unterstütztes Format %q", ext)
	}
}

func mp3Duration(r io.ReadSeeker) (time.Duration, error) {
	if err := skipID3v2(r); err != nil {
		return 0, err
	}
	dec := mp3.NewDecoder(bufio.NewReaderSize(r, 64*1024))
	var total time.Duration
	var frame mp3.Frame
	var skipped int
	for {
		err := dec.Decode(&frame, &skipped)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Many MP3 files end with a truncated frame or an ID3v1 tag. As
			// long as frames have already been read, keep the partial result.
			if total > 0 {
				break
			}
			return 0, err
		}
		total += frame.Duration()
	}
	if total <= 0 {
		return 0, errors.New("keine MP3-Audioframes gefunden")
	}
	return total, nil
}

// skipID3v2 moves r past any ID3v2 tag at the beginning of an MP3 file. This
// avoids interpreting bytes in embedded artwork as MPEG frames.
func skipID3v2(r io.ReadSeeker) error {
	for i := 0; i < 4; i++ {
		var header [10]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				_, seekErr := r.Seek(0, io.SeekStart)
				return seekErr
			}
			return err
		}
		if string(header[:3]) != "ID3" {
			_, err := r.Seek(0, io.SeekStart)
			return err
		}
		size := int64(header[6]&0x7f)<<21 |
			int64(header[7]&0x7f)<<14 |
			int64(header[8]&0x7f)<<7 |
			int64(header[9]&0x7f)
		if header[5]&0x10 != 0 { // ID3v2.4 footer
			size += 10
		}
		if _, err := r.Seek(size, io.SeekCurrent); err != nil {
			return err
		}
	}
	return nil
}

type oggPage struct {
	HeaderType      byte
	GranulePosition uint64
	SerialNumber    uint32
	Segments        []byte
}

// oggStreamInfo looks at the first packet of an Ogg stream to determine whether
// it is Vorbis or Opus and returns the stream serial number and Opus pre-skip.
func oggStreamInfo(f *os.File) (codec string, serial uint32, preSkip uint16, err error) {
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", 0, 0, err
	}
	page, data, err := readOggPage(f, true)
	if err != nil {
		return "", 0, 0, err
	}
	packet := firstOggPacket(page.Segments, data)
	switch {
	case bytes.HasPrefix(packet, []byte("OpusHead")):
		if len(packet) < 12 {
			return "", 0, 0, errors.New("ungültiger OpusHead")
		}
		return "opus", page.SerialNumber, binary.LittleEndian.Uint16(packet[10:12]), nil
	case len(packet) >= 7 && packet[0] == 1 && string(packet[1:7]) == "vorbis":
		return "vorbis", page.SerialNumber, 0, nil
	default:
		return "", 0, 0, errors.New("kein Ogg-Vorbis- oder Opus-Stream")
	}
}

func firstOggPacket(segments, data []byte) []byte {
	end := 0
	for _, size := range segments {
		end += int(size)
		if size < 255 {
			break
		}
	}
	if end > len(data) {
		end = len(data)
	}
	return data[:end]
}

func readOggPage(f *os.File, withData bool) (oggPage, []byte, error) {
	var header [27]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return oggPage{}, nil, err
	}
	if string(header[:4]) != "OggS" {
		return oggPage{}, nil, errors.New("keine Ogg-Seite gefunden")
	}
	page := oggPage{
		HeaderType:      header[5],
		GranulePosition: binary.LittleEndian.Uint64(header[6:14]),
		SerialNumber:    binary.LittleEndian.Uint32(header[14:18]),
	}
	segmentCount := int(header[26])
	page.Segments = make([]byte, segmentCount)
	if _, err := io.ReadFull(f, page.Segments); err != nil {
		return oggPage{}, nil, err
	}
	var size int64
	for _, segment := range page.Segments {
		size += int64(segment)
	}
	if !withData {
		if _, err := f.Seek(size, io.SeekCurrent); err != nil {
			return oggPage{}, nil, err
		}
		return page, nil, nil
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil {
		return oggPage{}, nil, err
	}
	return page, data, nil
}

func oggDuration(f *os.File) (time.Duration, error) {
	codec, serial, preSkip, err := oggStreamInfo(f)
	if err != nil {
		return 0, err
	}
	if codec == "opus" {
		return opusDuration(f, serial, preSkip)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	samples, format, err := oggvorbis.GetLength(f)
	if err != nil {
		return 0, err
	}
	if samples <= 0 || format == nil || format.SampleRate <= 0 {
		return 0, errors.New("keine gültige Ogg-Vorbis-Länge gefunden")
	}
	return secondsDuration(float64(samples) / float64(format.SampleRate)), nil
}

// opusDuration determines the length from the last Ogg granule position. Opus
// granules always use a 48 kHz clock and include the pre-skip samples in the
// first page, so the pre-skip has to be subtracted.
func opusDuration(f *os.File, serial uint32, preSkip uint16) (time.Duration, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var lastGranule uint64
	found := false
	for {
		page, _, err := readOggPage(f, false)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || found {
				break
			}
			return 0, err
		}
		if page.SerialNumber != serial || page.GranulePosition == math.MaxUint64 {
			continue
		}
		if page.GranulePosition > lastGranule {
			lastGranule = page.GranulePosition
			found = true
		}
	}
	if !found || lastGranule == 0 {
		return 0, errors.New("keine gültige Opus-Länge gefunden")
	}
	if lastGranule <= uint64(preSkip) {
		return 0, errors.New("ungültige Opus-Pre-Skip-Angabe")
	}
	samples := lastGranule - uint64(preSkip)
	return secondsDuration(float64(samples) / 48000), nil
}

func flacDuration(f *os.File) (time.Duration, error) {
	// Some FLAC files carry a (non-standard) ID3v2 tag in front of the
	// "fLaC" signature.
	if err := skipID3v2(f); err != nil {
		return 0, err
	}
	stream, err := flac.New(f)
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	if stream.Info == nil || stream.Info.SampleRate == 0 {
		return 0, errors.New("FLAC-StreamInfo fehlt")
	}
	if stream.Info.NSamples == 0 {
		return 0, errors.New("FLAC-Sampleanzahl ist unbekannt")
	}
	return secondsDuration(float64(stream.Info.NSamples) / float64(stream.Info.SampleRate)), nil
}

func m4aDuration(f *os.File) (time.Duration, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeMvhd(),
	})
	if err == nil && len(boxes) > 0 {
		if mvhd, ok := boxes[0].Payload.(*mp4.Mvhd); ok && mvhd.Timescale > 0 {
			if d := mvhd.GetDuration(); d > 0 {
				return secondsDuration(float64(d) / float64(mvhd.Timescale)), nil
			}
		}
	}

	// Fragmented or unusual files may not have a meaningful mvhd duration.
	// Fall back to the media header of the first track.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	boxes, err = mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd(),
	})
	if err != nil {
		return 0, err
	}
	var best float64
	for _, box := range boxes {
		mdhd, ok := box.Payload.(*mp4.Mdhd)
		if !ok || mdhd.Timescale == 0 {
			continue
		}
		seconds := float64(mdhd.GetDuration()) / float64(mdhd.Timescale)
		if seconds > best {
			best = seconds
		}
	}
	if best <= 0 {
		return 0, errors.New("keine gültige M4A-Dauer gefunden")
	}
	return secondsDuration(best), nil
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(math.Round(seconds*1000)) * time.Millisecond
}
