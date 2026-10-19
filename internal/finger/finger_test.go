package finger

import "testing"

func TestSimilarity(t *testing.T) {
	a := "AQADtEmkSYhEaFQoUqJEiQ4dOnQoUaJEiQ4dOnTo0KFChw4dOnQoUaJEiQ4dOnTo0KFChw4dOnQoUQ=="
	if got := Similarity(a, a); got != 1 {
		t.Fatalf("identical fingerprint similarity = %v, want 1", got)
	}
	if got := Similarity(a, ""); got != 0 {
		t.Fatalf("empty fingerprint similarity = %v, want 0", got)
	}
	b := "AQADtEmkSYhEaFQoUqJEiQ4dOnQoUaJEiQ4dOnTo0KFChw4dOnQoUaJEiQ4dOnTo0KFChw4dOnQoUQ=="
	if got := Similarity(a, b); got != 1 {
		t.Fatalf("identical-ish similarity = %v, want 1", got)
	}
	// 不同指纹相似度应显著低于 1
	c := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
	if got := Similarity(a, c); got > 0.5 {
		t.Fatalf("unrelated similarity = %v, want < 0.5", got)
	}
}
