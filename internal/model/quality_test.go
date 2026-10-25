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
		// —— 文档 P2 边界夹具：有损编码异常位深 / 高采样率异常 / MKA / 临界 ——
		{"lossy mp3 anomalous bitdepth", 256, 44100, 24, ".mp3", "高品"}, // 有损扩展名即使位深异常也不按无损
		{"lossy aac anomalous bitdepth", 128, 44100, 16, ".aac", "普通"},
		{"lossy high samplerate not hires", 320, 96000, 0, ".mp3", "高品"}, // 有损高采样率不因字段异常自动 Hi-Res
		{"lossy high samplerate+bitdepth", 320, 96000, 24, ".mp3", "高品"},
		{"mka no bitdepth lossless fallback", 320, 44100, 0, ".mka", "无损"}, // 容器假设（MKA 通常装无损），已知局限
		{"mka hires 24/96", 2304, 96000, 24, ".mka", "Hi-Res"},
		{"flac exactly 96k", 2304, 96000, 16, ".flac", "Hi-Res"},   // 采样率临界恰 96k
		{"flac exactly 24bit", 2304, 44100, 24, ".flac", "Hi-Res"}, // 位深临界恰 24
		{"lossy opus anomalous bitdepth", 128, 48000, 32, ".opus", "普通"},
		{"upper ext case", 1000, 44100, 16, ".FLAC", "无损"}, // 扩展名大小写不敏感
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
