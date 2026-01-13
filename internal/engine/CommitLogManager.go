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
	payloadLenBytes                = 4
	checksumBytes                  = 4
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

// Append new mutation durably to the commit log.
// Assigns sequence number to the mutation before encoding.
func (cm *CommitLogManager) Append(mut model.Mutation) error {
	// Assign sequence number to mutation (establishes total ordering)
	mut.Sequence = cm.flusher.seq_number

	encoded := cm.encodeMutation(mut)
	channelMsg := CommitLogChanelMsg{data: encoded, data_bufferred_done: make(chan error, 1)}
	select {
	case cm.commitlog_writter_channel <- channelMsg:
		// Wait until the channelMsg is buffered into commit log writer
		err := <-channelMsg.data_bufferred_done
		if err == nil {
			// Increment sequence number only on successful buffer write
			cm.flusher.seq_number++
		}
		return err

	case <-time.After(cm.cfg.EnqueueTimeoutInSecond):
		return errors.New("Timeout after waiting for mutation to be added to commit log")
	}
}

// Load the whole commit log file to build the mutation list.
// Stops at first corrupted or truncated record (crash-safe boundary).
func (cm *CommitLogManager) Load() []model.Mutation {
	mutations := make([]model.Mutation, 0)

	// Reopen file for reading (current handle is write-only)
	readFile, err := os.Open(cm.cfg.Path)
	if err != nil {
		log.Printf("failed to open commit log for reading: %v", err)
		return mutations
	}
	defer readFile.Close()

	// Get file size to detect truncation
	fileInfo, err := readFile.Stat()
	if err != nil {
		log.Printf("failed to stat commit log: %v", err)
		return mutations
	}
	fileSize := fileInfo.Size()

	if fileSize == 0 {
		return mutations // Empty file
	}

	var offset int64 = 0
	recordNum := 0

	for offset < fileSize {
		// Step 1: Read payload length (4 bytes)
		if offset+payloadLenBytes > fileSize {
			log.Printf("truncated at record %d: incomplete payload length at offset %d", recordNum, offset)
			break
		}

		lenBytes, err := storage.Read(readFile, offset, payloadLenBytes)
		if err != nil {
			log.Printf("error reading payload length at offset %d: %v", offset, err)
			break
		}
		if len(lenBytes) < payloadLenBytes {
			log.Printf("short read for payload length at offset %d", offset)
			break
		}
		payloadLen := binary.BigEndian.Uint32(lenBytes)
		offset += payloadLenBytes

		// Step 2: Read CRC32C checksum (4 bytes)
		if offset+checksumBytes > fileSize {
			log.Printf("truncated at record %d: incomplete checksum at offset %d", recordNum, offset)
			break
		}

		crcBytes, err := storage.Read(readFile, offset, checksumBytes)
		if err != nil {
			log.Printf("error reading checksum at offset %d: %v", offset, err)
			break
		}
		if len(crcBytes) < checksumBytes {
			log.Printf("short read for checksum at offset %d", offset)
			break
		}
		expectedChecksum := binary.BigEndian.Uint32(crcBytes)
		offset += checksumBytes

		// Step 3: Read payload (sequence through value)
		if offset+int64(payloadLen) > fileSize {
			log.Printf("truncated at record %d: incomplete payload at offset %d (expected %d bytes)",
				recordNum, offset, payloadLen)
			break
		}

		payload, err := storage.Read(readFile, offset, int(payloadLen))
		if err != nil {
			log.Printf("error reading payload at offset %d: %v", offset, err)
			break
		}
		if len(payload) < int(payloadLen) {
			log.Printf("short read for payload at offset %d", offset)
			break
		}
		offset += int64(payloadLen)

		// Step 4: Validate checksum
		actualChecksum := crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli))
		if actualChecksum != expectedChecksum {
			log.Printf("CRC mismatch at record %d: expected %x, got %x - stopping at corruption boundary",
				recordNum, expectedChecksum, actualChecksum)
			break
		}

		// Step 5: Decode payload fields
		mut, err := decodePayload(payload)
		if err != nil {
			log.Printf("failed to decode record %d: %v - stopping", recordNum, err)
			break
		}

		mutations = append(mutations, mut)
		recordNum++
	}

	log.Printf("loaded %d mutations from commit log (file size: %d bytes)", len(mutations), fileSize)
	return mutations
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
	// Reopen for reading (f is opened write-only in NewCommitLogManager)
	readFile, err := os.Open(f.Name())
	if err != nil {
		return 0 // File doesn't exist or can't be read, start from 0
	}
	defer readFile.Close()

	fileInfo, err := readFile.Stat()
	if err != nil || fileInfo.Size() == 0 {
		return 0 // Empty file, start from 0
	}

	var latestSeq uint64 = 0
	var offset int64 = 0
	fileSize := fileInfo.Size()

	for offset < fileSize {
		// Read payload length
		lenBytes, err := storage.Read(readFile, offset, payloadLenBytes)
		if err != nil || len(lenBytes) < payloadLenBytes {
			break // Stop at first read error
		}
		payloadLen := binary.BigEndian.Uint32(lenBytes)
		offset += payloadLenBytes + checksumBytes // Skip checksum

		// Read just the sequence number from payload (first 8 bytes)
		if offset+seqNumBytes > fileSize {
			break
		}
		seqBytes, err := storage.Read(readFile, offset, seqNumBytes)
		if err != nil || len(seqBytes) < seqNumBytes {
			break
		}
		seqNum := binary.BigEndian.Uint64(seqBytes)
		latestSeq = seqNum

		// Skip rest of payload
		offset += int64(payloadLen)
	}

	// Return next sequence number to use
	return latestSeq + 1
}

/*
Return encoded mutation record for Commit Log. The following table describes the structure of encoded mutation record.

The fields are described in the following order:

| PayloadLength | CRC32C | Sequence | OpType | KeyLen | Key      | ValueLen | Value    |
|--------------|--------|----------|--------|--------|----------|----------|----------|
| 4 bytes      | 4 bytes| 8 bytes  | 1 byte | 4 bytes| K bytes  | 4 bytes  | V bytes  |

The encoding process:
 1. Build payload which is []byte from "Sequence" to "Value"
 2. Compute CRC32C over payload.
 3. Write payload's length + CRC + payload in one buffered write (or small number of writes).
 4. fsync per durability policy.
*/
func (cm *CommitLogManager) encodeMutation(mut model.Mutation) []byte {
	payload := make([]byte, 0, seqNumBytes+opTypeBytes+lenFieldSize+len(mut.Key)+lenFieldSize+len(mut.Value))
	payload = append(payload, u64ToBytes(mut.Sequence)...)
	payload = append(payload, byte(mut.Op))
	payload = append(payload, u32ToBytes(uint32(len(mut.Key)))...)
	payload = append(payload, mut.Key...)
	payload = append(payload, u32ToBytes(uint32(len(mut.Value)))...)
	payload = append(payload, mut.Value...)

	payloadLenValue := u32ToBytes(uint32(len(payload)))
	checksum := u32ToBytes(crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli)))

	record := make([]byte, 0, payloadLenBytes+checksumBytes+len(payload))
	record = append(record, payloadLenValue...)
	record = append(record, checksum...)
	record = append(record, payload...)
	return record
}

// decodePayload extracts Mutation from the payload portion of a WAL record.
// Preserves the original sequence number for crash recovery (critical for last-write-wins).
// Payload structure (after length+CRC prefix):
//
//	| Sequence | OpType | KeyLen | Key      | ValueLen | Value    |
//	| 8 bytes  | 1 byte | 4 bytes| K bytes  | 4 bytes  | V bytes  |
func decodePayload(payload []byte) (model.Mutation, error) {
	minSize := seqNumBytes + opTypeBytes + lenFieldSize + lenFieldSize
	if len(payload) < minSize {
		return model.Mutation{}, fmt.Errorf("payload too short: %d bytes (minimum %d)", len(payload), minSize)
	}

	pos := 0

	// Read sequence number (MUST preserve original for last-write-wins)
	seqNum := binary.BigEndian.Uint64(payload[pos : pos+seqNumBytes])
	pos += seqNumBytes

	// Read OpType
	opType := model.OpsType(payload[pos])
	if opType != model.PUT && opType != model.DELETE {
		return model.Mutation{}, fmt.Errorf("invalid operation type: %d", opType)
	}
	pos += opTypeBytes

	// Read Key length and data
	keyLen := binary.BigEndian.Uint32(payload[pos : pos+lenFieldSize])
	pos += lenFieldSize

	if pos+int(keyLen) > len(payload) {
		return model.Mutation{}, fmt.Errorf("key length (%d) exceeds payload bounds", keyLen)
	}
	key := make([]byte, keyLen)
	copy(key, payload[pos:pos+int(keyLen)])
	pos += int(keyLen)

	// Read Value length and data
	if pos+lenFieldSize > len(payload) {
		return model.Mutation{}, fmt.Errorf("value length field exceeds payload bounds")
	}
	valueLen := binary.BigEndian.Uint32(payload[pos : pos+lenFieldSize])
	pos += lenFieldSize

	if pos+int(valueLen) > len(payload) {
		return model.Mutation{}, fmt.Errorf("value length (%d) exceeds payload bounds", valueLen)
	}

	var value []byte
	if valueLen > 0 {
		value = make([]byte, valueLen)
		copy(value, payload[pos:pos+int(valueLen)])
	}

	return model.Mutation{
		Op:       opType,
		Key:      key,
		Value:    value,
		Sequence: seqNum, // Preserve original sequence from WAL
	}, nil
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
