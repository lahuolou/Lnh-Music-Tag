// Package finger provides audio-content fingerprinting via Chromaprint's
// fpcalc CLI and similarity comparison between fingerprints. It is used to
// identify tracks that carry no usable tags or filename (e.g. "track01.mp3")
// by matching their audio content against already-tagged tracks in the library.
//
// fpcalc is bundled in the Docker image (apk add chromaprint); when it is
// absent the engine reports unavailable and identification degrades gracefully.
package finger

import (
	"context"
	"encoding/json"
	"math/bits"
	"os/exec"
	"strings"
	"time"
)

// fpTimeout 单次 fpcalc 调用的超时上限：异常二进制/坏文件卡死时
// 在超时后报错返回，不让识别任务永久假死。
const fpTimeout = 60 * time.Second

// Fpcalc wraps the Chromaprint fpcalc CLI. Callers control concurrency
// (fpcalc is a short-lived subprocess; a small worker pool is fine).
type Fpcalc struct {
	Bin string
}

func (f *Fpcalc) Available() bool {
	if f.Bin == "" {
		return false
	}
	_, err := exec.LookPath(f.Bin)
	return err == nil
}

// Fingerprint returns the raw Chromaprint fingerprint (space-separated ints)
// for a file. Raw fingerprints are used for local Hamming-distance matching.
func (f *Fpcalc) Fingerprint(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fpTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, f.Bin, "-raw", path).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CompressedFingerprint returns the compressed Chromaprint fingerprint
// (fpcalc -json) for a file. This is the format required by the AcoustID API.
func (f *Fpcalc) CompressedFingerprint(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fpTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, f.Bin, "-json", path).Output()
	if err != nil {
		return "", err
	}
	var r struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return "", err
	}
	return strings.TrimSpace(r.Fingerprint), nil
}

// Similarity returns a 0..1 score between two fingerprints: 1 means identical.
// It decodes the raw 32-bit ints and compares them with a normalized Hamming
// distance. Fingerprints of different lengths (different durations) compare
// over their common prefix.
func Similarity(a, b string) float64 {
	ai, bi := decode(a), decode(b)
	if len(ai) == 0 || len(bi) == 0 {
		return 0
	}
	n := len(ai)
	if len(bi) < n {
		n = len(bi)
	}
	diffs := 0
	for i := 0; i < n; i++ {
		diffs += bits.OnesCount32(ai[i] ^ bi[i])
	}
	total := n * 32
	return 1 - float64(diffs)/float64(total)
}

func decode(s string) []uint32 {
	var out []uint32
	var cur uint32
	flush := func() {
		if cur != 0 || len(out) > 0 {
			out = append(out, cur)
		}
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur = cur*10 + uint32(r-'0')
		} else {
			flush()
			cur = 0
		}
	}
	flush()
	return out
}
