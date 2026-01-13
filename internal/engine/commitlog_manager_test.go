package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"simplekv/internal/model"
)

func TestCommitLogFlushOnBufferLimit(t *testing.T) {
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "wal.log")

	cfg := CommitLogCfg{
		Path:                   walPath,
		EnqueueTimeoutInSecond: 500 * time.Millisecond,
		FlushIntervalInSecond:  30 * time.Second, // avoid periodic flush interference
		MaxEnqueuingMutation:   16,
		BufferBytes:            128, // small to trigger flush by size with crafted payloads
	}

	ctx := context.Background()
	mgr, cancel, err := NewCommitLogManager(ctx, cfg)
	if err != nil {
		t.Fatalf("create commit log manager: %v", err)
	}
	defer cancel()

	first := model.Mutation{Op: model.PUT, Key: []byte("k1"), Value: []byte("v1")}
	if err := mgr.Append(first); err != nil {
		t.Fatalf("append first: %v", err)
	}

	fmt.Printf("First size: %d-%d\n", len(first.Key), len(first.Value))

	if size := walFileSize(walPath); size != 0 {
		t.Fatalf("expected no flush after first append, got size %d", size)
	} else {
		fmt.Printf("commit log size: %d\n", size)
	}

	second := model.Mutation{
		Op:    model.PUT,
		Key:   bytes.Repeat([]byte("a"), 60),
		Value: bytes.Repeat([]byte("b"), 20),
	}
	if err := mgr.Append(second); err != nil {
		t.Fatalf("append second: %v", err)
	}

	fmt.Printf("Second size: %d-%d\n", len(second.Key), len(second.Value))

	// Here no need to sleep as the second append triggers the flushing
	// And as each append is a blocking call, we're sure that the first append is flushed
	if size := walFileSize(walPath); size == 0 {
		t.Fatalf("expected flush on buffer limit, got size %d", size)
	} else {
		fmt.Printf("commit log size: %d\n", size)
	}
}

func TestCommitLogFlushOnUnexpectedContextShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "wal.log")

	cfg := CommitLogCfg{
		Path:                   walPath,
		EnqueueTimeoutInSecond: 500 * time.Millisecond,
		FlushIntervalInSecond:  30 * time.Second, // avoid periodic flush interference
		MaxEnqueuingMutation:   16,
		BufferBytes:            128, // small to trigger flush by size with crafted payloads
	}

	ctx := context.Background()
	mgr, cancel, err := NewCommitLogManager(ctx, cfg)
	if err != nil {
		t.Fatalf("create commit log manager: %v", err)
	}

	first := model.Mutation{Op: model.PUT, Key: []byte("k1"), Value: []byte("v1")}
	if err := mgr.Append(first); err != nil {
		t.Fatalf("append first: %v", err)
	}

	fmt.Printf("First size: %d-%d\n", len(first.Key), len(first.Value))

	if size := walFileSize(walPath); size != 0 {
		t.Fatalf("expected no flush after first append, got size %d", size)
	}

	cancel()

	// Here no need to sleep as the cancel() will trigger the flush goroutine.
	time.Sleep(20 * time.Millisecond) // allow goroutine to finish flush
	if size := walFileSize(walPath); size == 0 {
		t.Fatalf("expected flush on after the context shutdown, got size %d", size)
	} else {
		fmt.Printf("commit log size: %d\n", size)
	}
}

func TestCommitLogFlushOnInterval(t *testing.T) {
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "wal.log")

	cfg := CommitLogCfg{
		Path:                   walPath,
		EnqueueTimeoutInSecond: 500 * time.Millisecond,
		FlushIntervalInSecond:  20 * time.Millisecond,
		MaxEnqueuingMutation:   16,
		BufferBytes:            1 << 20, // large to avoid size-based flush
	}

	ctx := context.Background()
	mgr, cancel, err := NewCommitLogManager(ctx, cfg)
	if err != nil {
		t.Fatalf("create commit log manager: %v", err)
	}
	defer cancel()

	mut := model.Mutation{Op: model.PUT, Key: []byte("k1"), Value: []byte("v1")}
	if err := mgr.Append(mut); err != nil {
		t.Fatalf("append: %v", err)
	}

	if size := walFileSize(walPath); size != 0 {
		t.Fatalf("expected buffered data before interval flush, got size %d", size)
	}

	time.Sleep(cfg.FlushIntervalInSecond + 10*time.Millisecond)

	if size := walFileSize(walPath); size == 0 {
		t.Fatalf("expected periodic flush to write data, got size %d", size)
	} else {
		fmt.Printf("Final commit log: %d\n", size)
	}
}

func TestCommitLogLoad(t *testing.T) {
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "wal.log")

	cfg := CommitLogCfg{
		Path:                   walPath,
		EnqueueTimeoutInSecond: 500 * time.Millisecond,
		FlushIntervalInSecond:  30 * time.Second,
		MaxEnqueuingMutation:   16,
		BufferBytes:            1 << 20, // large buffer to avoid size-based flush
	}

	// Phase 1: Write mutations to commit log
	ctx := context.Background()
	mgr, cancel, err := NewCommitLogManager(ctx, cfg)
	if err != nil {
		t.Fatalf("create commit log manager: %v", err)
	}

	// Prepare test mutations with mix of PUT and DELETE operations
	mutations := []model.Mutation{
		{Op: model.PUT, Key: []byte("key1"), Value: []byte("value1")},
		{Op: model.PUT, Key: []byte("key2"), Value: []byte("value2")},
		{Op: model.DELETE, Key: []byte("key1"), Value: nil},
		{Op: model.PUT, Key: []byte("key3"), Value: []byte("longer-value-here-for-testing")},
		{Op: model.DELETE, Key: []byte("key2"), Value: []byte("")},
	}

	// Append all mutations
	for i, mut := range mutations {
		if err := mgr.Append(mut); err != nil {
			t.Fatalf("append mutation %d: %v", i, err)
		}
	}

	// Force flush by canceling context (simulates shutdown)
	cancel()
	time.Sleep(50 * time.Millisecond) // allow goroutine to finish flush and cleanup

	// Verify data was written to disk
	if size := walFileSize(walPath); size == 0 {
		t.Fatalf("expected commit log to be flushed, got size 0")
	} else {
		fmt.Printf("Commit log size after flush: %d bytes\n", size)
	}

	// Phase 2: Load mutations from commit log (simulates crash recovery)
	ctx2 := context.Background()
	mgr2, cancel2, err := NewCommitLogManager(ctx2, cfg)
	if err != nil {
		t.Fatalf("create second commit log manager: %v", err)
	}
	defer cancel2()

	loaded := mgr2.Load()

	// Verify loaded mutations match original mutations
	if len(loaded) != len(mutations) {
		t.Fatalf("loaded mutation count mismatch: got %d, want %d", len(loaded), len(mutations))
	}

	for i, original := range mutations {
		got := loaded[i]

		if got.Op != original.Op {
			t.Errorf("mutation[%d] Op mismatch: got %v, want %v", i, got.Op, original.Op)
		}

		if !bytes.Equal(got.Key, original.Key) {
			t.Errorf("mutation[%d] Key mismatch: got %q, want %q", i, got.Key, original.Key)
		}

		if !bytes.Equal(got.Value, original.Value) {
			t.Errorf("mutation[%d] Value mismatch: got %q, want %q", i, got.Value, original.Value)
		}
	}
}

func TestCommitLogLoadEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "empty.log")

	cfg := CommitLogCfg{
		Path:                   walPath,
		EnqueueTimeoutInSecond: 500 * time.Millisecond,
		FlushIntervalInSecond:  30 * time.Second,
		MaxEnqueuingMutation:   16,
		BufferBytes:            1 << 20,
	}

	ctx := context.Background()
	mgr, cancel, err := NewCommitLogManager(ctx, cfg)
	if err != nil {
		t.Fatalf("create commit log manager: %v", err)
	}
	defer cancel()

	// Load from empty file should return empty slice
	loaded := mgr.Load()

	if loaded == nil {
		t.Fatal("Load() returned nil, expected empty slice")
	}

	if len(loaded) != 0 {
		t.Fatalf("Load() on empty file returned %d mutations, expected 0", len(loaded))
	}
}

func TestCommitLogLoadAfterMultipleFlushes(t *testing.T) {
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "multi-flush.log")

	cfg := CommitLogCfg{
		Path:                   walPath,
		EnqueueTimeoutInSecond: 500 * time.Millisecond,
		FlushIntervalInSecond:  20 * time.Millisecond, // periodic flush
		MaxEnqueuingMutation:   16,
		BufferBytes:            128, // small buffer to trigger multiple flushes
	}

	ctx := context.Background()
	mgr, cancel, err := NewCommitLogManager(ctx, cfg)
	if err != nil {
		t.Fatalf("create commit log manager: %v", err)
	}

	// Append mutations that will trigger multiple flushes
	mutations := []model.Mutation{
		{Op: model.PUT, Key: []byte("a"), Value: []byte("v1")},
		{Op: model.PUT, Key: bytes.Repeat([]byte("b"), 40), Value: bytes.Repeat([]byte("x"), 30)}, // triggers flush
		{Op: model.DELETE, Key: []byte("c"), Value: nil},
		{Op: model.PUT, Key: bytes.Repeat([]byte("d"), 40), Value: bytes.Repeat([]byte("y"), 30)}, // triggers flush
	}

	for i, mut := range mutations {
		if err := mgr.Append(mut); err != nil {
			t.Fatalf("append mutation %d: %v", i, err)
		}
	}

	// Ensure final flush
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Load and verify all mutations persisted across multiple flushes
	ctx2 := context.Background()
	mgr2, cancel2, err := NewCommitLogManager(ctx2, cfg)
	if err != nil {
		t.Fatalf("create second commit log manager: %v", err)
	}
	defer cancel2()

	loaded := mgr2.Load()

	if len(loaded) != len(mutations) {
		t.Fatalf("loaded mutation count mismatch after multiple flushes: got %d, want %d", len(loaded), len(mutations))
	}

	for i, original := range mutations {
		got := loaded[i]
		if got.Op != original.Op || !bytes.Equal(got.Key, original.Key) || !bytes.Equal(got.Value, original.Value) {
			t.Errorf("mutation[%d] mismatch: got {%v, %q, %q}, want {%v, %q, %q}",
				i, got.Op, got.Key, got.Value, original.Op, original.Key, original.Value)
		}
	}

	fmt.Printf("Successfully loaded %d mutations across multiple flushes\n", len(loaded))
}

func TestCommitLogLoadPreservesSequenceNumbers(t *testing.T) {
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "sequence-test.log")

	cfg := CommitLogCfg{
		Path:                   walPath,
		EnqueueTimeoutInSecond: 500 * time.Millisecond,
		FlushIntervalInSecond:  30 * time.Second,
		MaxEnqueuingMutation:   16,
		BufferBytes:            1 << 20,
	}

	// Phase 1: Write mutations with known sequence numbers
	ctx := context.Background()
	mgr, cancel, err := NewCommitLogManager(ctx, cfg)
	if err != nil {
		t.Fatalf("create commit log manager: %v", err)
	}

	// Write same key multiple times to test last-write-wins
	mutations := []model.Mutation{
		{Op: model.PUT, Key: []byte("key1"), Value: []byte("value1")},   // seq=0
		{Op: model.PUT, Key: []byte("key2"), Value: []byte("value2")},   // seq=1
		{Op: model.PUT, Key: []byte("key1"), Value: []byte("updated1")}, // seq=2 (should win for key1)
		{Op: model.DELETE, Key: []byte("key2"), Value: nil},             // seq=3 (delete key2)
		{Op: model.PUT, Key: []byte("key3"), Value: []byte("value3")},   // seq=4
	}

	for i, mut := range mutations {
		if err := mgr.Append(mut); err != nil {
			t.Fatalf("append mutation %d: %v", i, err)
		}
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	// Phase 2: Load and verify sequence numbers are preserved
	ctx2 := context.Background()
	mgr2, cancel2, err := NewCommitLogManager(ctx2, cfg)
	if err != nil {
		t.Fatalf("create second commit log manager: %v", err)
	}
	defer cancel2()

	loaded := mgr2.Load()

	if len(loaded) != len(mutations) {
		t.Fatalf("loaded mutation count mismatch: got %d, want %d", len(loaded), len(mutations))
	}

	// Verify each mutation has correct sequence number
	for i, mut := range loaded {
		expectedSeq := uint64(i)
		if mut.Sequence != expectedSeq {
			t.Errorf("mutation[%d] sequence mismatch: got %d, want %d", i, mut.Sequence, expectedSeq)
		}
		fmt.Printf("Mutation %d: key=%q, value=%q, seq=%d, op=%v\n",
			i, mut.Key, mut.Value, mut.Sequence, mut.Op)
	}

	// Verify that the next sequence number continues correctly
	if mgr2.flusher.seq_number != uint64(len(mutations)) {
		t.Errorf("next sequence number mismatch: got %d, want %d",
			mgr2.flusher.seq_number, len(mutations))
	}

	fmt.Printf("\nLast-write-wins demonstration:\n")
	fmt.Printf("- key1: seq=0 (value1) -> seq=2 (updated1) [winner]\n")
	fmt.Printf("- key2: seq=1 (value2) -> seq=3 (DELETE) [deleted]\n")
	fmt.Printf("- key3: seq=4 (value3) [only version]\n")
	fmt.Printf("Next write will use sequence number: %d\n", mgr2.flusher.seq_number)
}

func walFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
