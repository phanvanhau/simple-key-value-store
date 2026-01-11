package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"log"
	"os"
	"simplekv/internal/model"
	"simplekv/internal/storage"
	"time"
)

type CommitLogFlusher struct {
	active_segment *os.File
	seq_number     uint64
	buffer         bytes.Buffer
	maxBufferBytes int
}

type CommitLogChanelMsg struct {
	data                []byte
	data_bufferred_done chan error
}

type CommitLogCfg struct {
	Path                   string
	EnqueueTimeoutInSecond time.Duration
	FlushIntervalInSecond  time.Duration
	MaxEnqueuingMutation   int
	BufferBytes            int
}

/*
Channel-backed append flow keeps a single writer goroutine in charge of the WAL:
- Ordering: channel preserves request order; single goroutine owns the file handle.
- Simplicity: avoids mutexes; only the writer goroutine touches the buffer/file.
- Backpressure: bounded channel + timeout lets callers fail fast instead of unbounded queueing.
- Durability handshake: per-request done channel lets callers wait for accept/flush/fsync.
- Shutdown: select on context to flush outstanding data before exit without racing writers.
*/
type CommitLogManager struct {
	// Implement the CommitLogManager structure here
	flusher                   CommitLogFlusher
	commitlog_writter_channel chan CommitLogChanelMsg
	cfg                       CommitLogCfg
	flushT                    *time.Ticker
}

const (
	seqNumBytes                    = 8
	opTypeBytes                    = 1
	lenFieldSize                   = 4
	defaultCommitLogBufferBytes    = 4 * 1024 * 1024
	minimalCommitLogBufferBytes    = 128
	defaultMaxEnqueuingMutationVal = 1024
)

func NewCommitLogManager(ctx context.Context, cfg CommitLogCfg) (*CommitLogManager, context.CancelFunc, error) {
	f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}

	seg_number := get_latest_seq_num(f)

	bufferBytes := cfg.BufferBytes
	if bufferBytes <= 0 {
		bufferBytes = defaultCommitLogBufferBytes
	}
	if bufferBytes < minimalCommitLogBufferBytes {
		bufferBytes = minimalCommitLogBufferBytes
	}

	maxQueue := cfg.MaxEnqueuingMutation
	if maxQueue <= 0 {
		maxQueue = defaultMaxEnqueuingMutationVal
	}

	m := &CommitLogManager{
		cfg:                       cfg,
		commitlog_writter_channel: make(chan CommitLogChanelMsg, maxQueue),
		flushT:                    time.NewTicker(cfg.FlushIntervalInSecond),
		flusher: CommitLogFlusher{
			active_segment: f,
			seq_number:     seg_number,
			buffer:         bytes.Buffer{},
			maxBufferBytes: bufferBytes,
		},
	}

	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		m.run(runCtx)
		m.flushT.Stop()
		m.flusher.flush()
		_ = m.flusher.active_segment.Close()
	}()
	return m, cancel, nil
}

func (cm *CommitLogManager) Append(mut model.Mutation) error {
	encoded := cm.encodeMutation(mut)
	channelMsg := CommitLogChanelMsg{data: encoded, data_bufferred_done: make(chan error, 1)}
	select {
	case cm.commitlog_writter_channel <- channelMsg:
		// we should wait until the channelMsg is buffered into commit log writter
		return <-channelMsg.data_bufferred_done
	case <-time.After(cm.cfg.EnqueueTimeoutInSecond):
		return errors.New("Timeout after waiting for mutation to be added to commit log")
	}
}

func (cm *CommitLogManager) run(ctx context.Context) {
	for {
		select {
		case channelMsg := <-cm.commitlog_writter_channel:
			// Critical path: Should append commit log to buffer for fail
			err := cm.flusher.write(channelMsg.data)
			channelMsg.data_bufferred_done <- err // success/error about buffering process
		case <-cm.flushT.C:
			log.Printf("Periodical Flush - Flushing active commit log segment")
			if err := cm.flusher.flush(); err != nil {
				log.Printf("commit log periodic flush error: %v", err)
			}
		case <-ctx.Done():
			log.Printf("Commit log manager is shutting down - Flushing active commit log segment")
			if err := cm.flusher.flush(); err != nil {
				log.Printf("commit log shutdown flush error: %v", err)
			}
			return
		}
	}
}

func (flusher *CommitLogFlusher) write(data []byte) error {
	if flusher.active_segment == nil {
		return errors.New("no active segment")
	}

	if len(data) > flusher.maxBufferBytes {
		return fmt.Errorf("commit log entry (%d bytes) exceeds buffer size (%d bytes)", len(data), flusher.maxBufferBytes)
	}

	if flusher.buffer.Len()+len(data) > flusher.maxBufferBytes {
		if err := flusher.flush(); err != nil {
			return err
		}
	}

	_, err := flusher.buffer.Write(data)
	return err
}

func (flusher *CommitLogFlusher) flush() error {
	if flusher.active_segment == nil {
		return errors.New("no active segment")
	}
	if flusher.buffer.Len() == 0 {
		return nil
	}

	if err := storage.Write(flusher.active_segment, flusher.buffer.Bytes()); err != nil {
		return err
	}
	err := flusher.active_segment.Sync()
	if err == nil {
		flusher.buffer.Reset()
	}
	return err
}

func get_latest_seq_num(f *os.File) uint64 {
	// TODO: Implement this function by parsing the latest active commit file and take the latest appended mutation seq number
	return 0
}

/*
	Return encoded mutation record for Commit Log. The following table describes the structure of encoded mutation record.

The fields are described in the following order:

| RecordLength | CRC32C | Sequence | OpType | KeyLen | Key      | ValueLen | Value    |
|--------------|--------|----------|--------|--------|----------|----------|----------|
| 4 bytes      | 4 bytes| 8 bytes  | 1 byte | 4 bytes| K bytes  | 4 bytes  | V bytes  |

The encoding process is:
 1. Build payload which is []byte from "Sequence" to "Value"
 2. Compute CRC32C over payload.
 3. Write length + CRC + payload in one buffered write (or small number of writes).
 4. fsync per durability policy.
*/
func (cm *CommitLogManager) encodeMutation(mut model.Mutation) []byte {
	payload := make([]byte, 0, seqNumBytes+opTypeBytes+lenFieldSize+len(mut.Key)+lenFieldSize+len(mut.Value))
	payload = append(payload, u64ToBytes(cm.flusher.seq_number)...)
	payload = append(payload, byte(mut.Op))
	payload = append(payload, u32ToBytes(uint32(len(mut.Key)))...)
	payload = append(payload, mut.Key...)
	payload = append(payload, u32ToBytes(uint32(len(mut.Value)))...)
	payload = append(payload, mut.Value...)

	payloadLen := u32ToBytes(uint32(len(payload)))
	checksum := u32ToBytes(crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli)))

	record := make([]byte, 0, len(payloadLen)+len(checksum)+len(payload))
	record = append(record, payloadLen...)
	record = append(record, checksum...)
	record = append(record, payload...)
	return record
}

func u64ToBytes(v uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v) // or LittleEndian as needed
	return buf[:]
}

func u32ToBytes(v uint32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v) // or LittleEndian as needed
	return buf[:]
}
