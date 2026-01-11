package e2e

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestPutGetDeleteLifecycle(t *testing.T) {
	sut := startSystemUnderTest(t)
	defer sut.Close()
	client := NewClient(sut.BaseURL, nil)
	ctx := testContext(t)

	key := []byte("alpha")
	value1 := []byte("one")
	value2 := []byte("two")

	if _, err := client.Put(ctx, key, value1); err != nil {
		t.Fatalf("put v1: %v", err)
	}
	got, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if !bytes.Equal(got.Value, value1) {
		t.Fatalf("value mismatch: got %q want %q", got.Value, value1)
	}

	if _, err := client.Put(ctx, key, value2); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	got, err = client.Get(ctx, key)
	if err != nil {
		t.Fatalf("get v2: %v", err)
	}
	if !bytes.Equal(got.Value, value2) {
		t.Fatalf("value mismatch after overwrite: got %q want %q", got.Value, value2)
	}

	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := client.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

func TestRangeScanOrdered(t *testing.T) {
	sut := startSystemUnderTest(t)
	defer sut.Close()
	client := NewClient(sut.BaseURL, nil)
	ctx := testContext(t)

	items := []KeyValue{
		{Key: []byte("a"), Value: []byte("va")},
		{Key: []byte("b"), Value: []byte("vb")},
		{Key: []byte("c"), Value: []byte("vc")},
	}
	for _, kv := range items {
		if _, err := client.Put(ctx, kv.Key, kv.Value); err != nil {
			t.Fatalf("seed put %q: %v", kv.Key, err)
		}
	}

	got, err := client.Scan(ctx, []byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	expected := []KeyValue{
		{Key: []byte("a"), Value: []byte("va")},
		{Key: []byte("b"), Value: []byte("vb")},
	}
	if len(got) != len(expected) {
		t.Fatalf("scan length: got %d want %d", len(got), len(expected))
	}
	for i := range expected {
		if !bytes.Equal(got[i].Key, expected[i].Key) || !bytes.Equal(got[i].Value, expected[i].Value) {
			t.Fatalf("scan[%d] mismatch: got %q:%q want %q:%q", i, got[i].Key, got[i].Value, expected[i].Key, expected[i].Value)
		}
	}
}

func TestIdempotentWrites(t *testing.T) {
	sut := startSystemUnderTest(t)
	defer sut.Close()
	client := NewClient(sut.BaseURL, nil)
	ctx := testContext(t)

	key := []byte("idempotent-key")
	value := []byte("payload")

	for i := 0; i < 3; i++ {
		if _, err := client.Put(ctx, key, value); err != nil {
			t.Fatalf("put attempt %d: %v", i, err)
		}
	}

	got, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("get after idempotent writes: %v", err)
	}
	if !bytes.Equal(got.Value, value) {
		t.Fatalf("value mismatch: got %q want %q", got.Value, value)
	}
}

func TestConcurrentLastWriteWins(t *testing.T) {
	sut := startSystemUnderTest(t)
	defer sut.Close()
	client := NewClient(sut.BaseURL, nil)
	ctx := testContext(t)

	key := []byte("concurrent-key")
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		val := []byte{byte('0' + i)}
		go func(v []byte) {
			defer wg.Done()
			_, _ = client.Put(ctx, key, v)
		}(val)
	}
	wg.Wait()

	final := []byte("winner")
	if _, err := client.Put(ctx, key, final); err != nil {
		t.Fatalf("final put: %v", err)
	}
	got, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("get final: %v", err)
	}
	if !bytes.Equal(got.Value, final) {
		t.Fatalf("last-write-wins violated: got %q want %q", got.Value, final)
	}
}

func TestDeleteRemovesFromScan(t *testing.T) {
	sut := startSystemUnderTest(t)
	defer sut.Close()
	client := NewClient(sut.BaseURL, nil)
	ctx := testContext(t)

	k1 := []byte("scan-a")
	k2 := []byte("scan-b")
	if _, err := client.Put(ctx, k1, []byte("one")); err != nil {
		t.Fatalf("put k1: %v", err)
	}
	if _, err := client.Put(ctx, k2, []byte("two")); err != nil {
		t.Fatalf("put k2: %v", err)
	}

	if err := client.Delete(ctx, k1); err != nil {
		t.Fatalf("delete k1: %v", err)
	}

	results, err := client.Scan(ctx, []byte("scan-a"), []byte("scan-z"))
	if err != nil {
		t.Fatalf("scan after delete: %v", err)
	}
	if len(results) != 1 || !bytes.Equal(results[0].Key, k2) {
		t.Fatalf("expected only k2 in scan, got %v", results)
	}
}

func TestBadRequestOnMalformedKey(t *testing.T) {
	sut := startSystemUnderTest(t)
	defer sut.Close()
	ctx := testContext(t)

	// Send a raw request with an intentionally invalid base64 key.
	endpoint := sut.BaseURL + "/v1/kv/not-base64??"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("build malformed request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do malformed request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed key, got %d", resp.StatusCode)
	}
}

func TestCrashRecovery(t *testing.T) {
	sut := startSystemUnderTest(t)
	defer sut.Close()
	if sut.restart == nil {
		t.Skip("restart testing requires a controllable server process")
	}

	client := NewClient(sut.BaseURL, nil)
	ctx := testContext(t)
	key := []byte("crash-key")
	value := []byte("persist-me")

	if _, err := client.Put(ctx, key, value); err != nil {
		t.Fatalf("put before crash: %v", err)
	}

	sut.restart(t)

	got, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if !bytes.Equal(got.Value, value) {
		t.Fatalf("crash recovery lost data: got %q want %q", got.Value, value)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
