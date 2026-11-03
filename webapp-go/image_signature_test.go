package isureal

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"net/url"
	"strconv"
	"testing"
)

func referenceImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	for range imageSignatureIterations {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func TestImageSignatureCompatibility(t *testing.T) {
	ResetSignatureCache()
	hashBytes := sha256.Sum256([]byte("test-secret"))
	key := hashBytes[:]
	imageID := "550e8400-e29b-41d4-a716-446655440000"
	exp := int64(1793500000)

	want := referenceImageSignature(key, imageID, exp)
	got1 := imageSignature(key, imageID, exp)
	got2 := imageSignature(key, imageID, exp) // cached path

	if !bytes.Equal(want, got1) {
		t.Fatalf("first call mismatch: got %x, want %x", got1, want)
	}
	if !bytes.Equal(want, got2) {
		t.Fatalf("cached call mismatch: got %x, want %x", got2, want)
	}

	user := User{ImageKey: key}
	u := signedImageURL(user, imageID)
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("failed to parse url: %v", err)
	}
	if parsed.Path != "/api/images/"+imageID {
		t.Fatalf("unexpected path: %s", parsed.Path)
	}
	if parsed.Query().Get("exp") == "" || parsed.Query().Get("sig") == "" {
		t.Fatalf("missing exp or sig in %s", u)
	}
}

// BenchmarkReferenceImageSignature 出題時の 1000 回ループ実装の速度
func BenchmarkReferenceImageSignature(b *testing.B) {
	key := []byte("secret-key-32bytes-padding!!!!!!")
	imageID := "550e8400-e29b-41d4-a716-446655440000"
	exp := int64(1793500000)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = referenceImageSignature(key, imageID, exp)
	}
}

// BenchmarkPrecomputedImageSignature 優勝者の O(1) 事前計算ルックアップ実装の速度
func BenchmarkPrecomputedImageSignature(b *testing.B) {
	key := []byte("secret-key-32bytes-padding!!!!!!")
	imageID := "550e8400-e29b-41d4-a716-446655440000"
	_ = PrecomputeImageSignature(key, imageID)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = PrecomputeImageSignature(key, imageID)
	}
}
