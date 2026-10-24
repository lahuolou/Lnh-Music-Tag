package model

import "testing"

func TestComputeQuality(t *testing.T) {
	cases := []struct {
		name       string
		bitrate    uint
		sampleRate uint
		bitDepth   uint
		ext        string
		want       string
	}{
		{"lossy 96k", 96, 44100, 0, ".mp3", "流畅"},
		{"lossy 128k", 128, 44100, 0, ".mp3", "普通"},
		{"lossy 192k", 192, 44100, 0, ".aac", "普通"},
		{"lossy 256k", 256, 44100, 0, ".mp3", "高品"},
		{"lossy 320k", 320, 44100, 0, ".mp3", "高品"},
		{"flac cd", 1000, 44100, 16, ".flac", "无损"},
		{"flac hires 24/96", 2304, 96000, 24, ".flac", "Hi-Res"},
		{"flac hires 192k", 4000, 192000, 16, ".flac", "Hi-Res"},
		{"wav no bitdepth fallback", 1411, 44100, 0, ".wav", "无损"},
		{"unknown props", 0, 0, 0, ".mp3", "未知"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Track{Bitrate: c.bitrate, SampleRate: c.sampleRate, BitDepth: c.bitDepth, Ext: c.ext}
			if got := tr.ComputeQuality(); got != c.want {
				t.Fatalf("ComputeQuality() = %q, want %q", got, c.want)
			}
		})
	}
}
