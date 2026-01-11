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

func walFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
