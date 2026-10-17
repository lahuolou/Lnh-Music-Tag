// Package audiofmt detects the real audio encoding of a file from its header
// magic bytes, returning the canonical file extension. This is used to clean up
// filenames that carry redundant or mismatched audio extensions (e.g.
// "发如雪.mp3.flac" -> "发如雪.flac") by checking the actual format first.
package audiofmt

import (
	"bytes"
	"os"
)

// ExtSet lists the canonical extensions audiofmt can recognize. Files whose
// extension is not in this set are not considered audio and are left untouched.
var ExtSet = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true, ".mp4": true, ".ogg": true,
	".opus": true, ".oga": true, ".wav": true, ".wma": true, ".wv": true,
	".ape": true, ".aiff": true, ".aif": true,
}

// DetectExt reads the header of path and returns the canonical extension for
// the real encoding (e.g. ".flac", ".mp3", ".m4a"), or "" if it cannot be
// reliably determined (not an audio file, unreadable, or unknown).
func DetectExt(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if len(buf) < 4 {
		return ""
	}
	switch {
	// ID3v2 tag -> MP3
	case bytes.HasPrefix(buf, []byte("ID3")):
		return ".mp3"
	// FLAC
	case bytes.HasPrefix(buf, []byte("fLaC")):
		return ".flac"
	// Ogg container (Vorbis / Opus / FLAC-in-Ogg). Keep .ogg for audio.
	case bytes.HasPrefix(buf, []byte("OggS")):
		return ".ogg"
	// RIFF/WAVE
	case bytes.HasPrefix(buf, []byte("RIFF")) && len(buf) >= 12 && string(buf[8:12]) == "WAVE":
		return ".wav"
	// MP4/M4A (brand at bytes 4..7)
	case len(buf) >= 8 && string(buf[4:8]) == "ftyp":
		return ".m4a"
	// ASF (WMA)
	case bytes.HasPrefix(buf, []byte{0x30, 0x26, 0xB2, 0x75}):
		return ".wma"
	// Monkey's Audio (APE)
	case bytes.HasPrefix(buf, []byte("MAC ")):
		return ".ape"
	// AIFF
	case bytes.HasPrefix(buf, []byte("FORM")) && len(buf) >= 12 && string(buf[8:12]) == "AIFF":
		return ".aiff"
	// MPEG audio frame sync (MP3 without an ID3 tag)
	case buf[0] == 0xFF && (buf[1]&0xE0) == 0xE0:
		return ".mp3"
	}
	return ""
}
