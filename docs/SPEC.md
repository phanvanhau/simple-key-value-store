# Overall system requirements
## Database engine
* Database will work with arbitrary data type for both key and value.
* Durability guarantees: Every write will be durable which is free of data loss by event like power lost. It could be archieved by multiple layered components:
** Write request will be returned successfully if mutation is added into Memtable and commit log
** Data in Memtable would be flushed into disk once reaching a size threshold
** Commit log maitain a buffer which will flush this buffer into disk if reaching threshold or meeting a configured criteria which is the flushing interval.
** Commit log must have a mechasnim to know which mutation had been flushed from MemTables to SSTable files so that the database server only maintain the active commit logs, whereas what called archived commit logs contain only mutations already flushed in disk.
* All write requests are idempotent
* When the node is crashed, restarting the node will require the database server to read the commitlog

## Database operations
The database support the following operations:
- READ "key": Read a single value from the specified "key"
- WRITE "key" "value": Write the specified key value pair into the system. In case of existing "key", the write operation is consider as an "update". In anycase, it create new mutation in the system where the database server will do required work to ensure the last write always win.
- DELETE "key": Delete data represented by "key"
- SCAN "fromKey" "endKey": Perform scanning to fetch values between key ranged closedly from "fromKey" to openly "endKey"

## Database connectivity
Use REST API as network connectivity from client to the database server which expose REST endpoints matching the data base operations.

## Success criteria and SLAs
* Durability: WAL fsync enabled by default on every write (or small batches), configurable `fsync_interval_ms` for throughput-oriented deployments. A write is acknowledged only after it is in both memtable and WAL.
* Consistency: single-writer per node with last-write-wins via monotonic sequence numbers; reads on the same node are read-your-writes.
* Latency targets (per node, SSD, moderate load): P50 GET/PUT sub-ms to low single-digit ms; P99 GET/PUT 10–50 ms; SCAN bound by data size.
* Limits: document supported max key length and value size (default up to a few MB); practical per-node dataset target in hundreds of GBs to TB on SSD.
* Availability: health endpoint returns 200 when WAL/fsync/compaction are healthy; return 503 if stalled.

## Observability
The database server must expose metrics and logs for monitoring. Metrics should include:
* WAL/fsync/compaction health status.
* Latency distribution for different operations: Commit logs flushing, compaction process elapsed time
* Counters for different key data: number of SStable per level, number of inflight mutations in MemTables not being flushed in SSTables, number of inflight mutations in commit logs not being flushed in disk
* Read/Write latency 

## Serialization and on-disk versioning
* WAL framing: length-prefixed records (big-endian) with CRC32C checksum, sequence number, op type (put/delete), key, value bytes.
* SSTable layout: magic + version in header/footer; footer points to index/meta blocks; per-block checksums; smallest/largest key for pruning.
* Versioning: include format version in WAL and SSTables; refuse or migrate on version mismatch for forward compatibility.

### WAL record layout

#### Framing view (sizes only):

| RecordLength | CRC32C | Sequence | OpType | KeyLen | Key      | ValueLen | Value    |
|--------------|--------|----------|--------|--------|----------|----------|----------|
| 4 bytes      | 4 bytes| 8 bytes  | 1 byte | 4 bytes| K bytes  | 4 bytes  | V bytes  |

PUT example with key = `0x61 0x62` ("ab"), value = `0x01 0x02 0x03`, sequence = 42, OpType = 0 (Put). CRC32C shown as placeholder.

#### Example
##### Step by step
Example WAL record layout (single PUT) with concrete byte arrays:

  - Key bytes: key = []byte{0x61, 0x62} (“ab”)
  - Value bytes: val = []byte{0x01, 0x02, 0x03}
  - Sequence: 42
  - OpType: 0x00 (Put)

  Payload fields concatenated (before CRC):

  [Sequence (8 bytes, big-endian)]        => 00 00 00 00 00 00 00 2A
  [OpType (1 byte)]                       => 00
  [KeyLen (4 bytes, big-endian)]          => 00 00 00 02
  [Key bytes]                             => 61 62
  [ValueLen (4 bytes, big-endian)]        => 00 00 00 03
  [Value bytes]                           => 01 02 03

  Payload hex (total 8+1+4+2+4+3 = 22 bytes):

  00 00 00 00 00 00 00 2A 00 00 00 00 02 61 62 00 00 00 03 01 02 03

  Compute CRC32C over that payload: suppose it’s DE AD BE EF (hex) for illustration.

  Record framing:

  [RecordLength (4 bytes, big-endian)] => 00 00 00 16   # 22 decimal
  [CRC32C (4 bytes, big-endian)]       => DE AD BE EF   # placeholder
  [Payload (22 bytes)]                 => 00 00 00 00 00 00 00 2A 00 00 00 00 02 61 62 00 00 00 03 01 02 03

  On disk (hex):

  00 00 00 16 DE AD BE EF 00 00 00 00 00 00 00 2A 00 00 00 00 02 61 62 00 00 00 03 01 02 03

  When writing:

  1. Build payload.
  2. Compute CRC32C over payload.
  3. Write length + CRC + payload in one buffered write (or small number of writes).
  4. fsync per durability policy.

##### Result WAL record

| Field                  | Size      | Value (hex)                 | Notes                               |
|------------------------|-----------|-----------------------------|-------------------------------------|
| RecordLength           | 4 bytes   | `00 00 00 16`               | Length of payload (22 bytes)        |
| CRC32C                 | 4 bytes   | `DE AD BE EF`               | Placeholder checksum over payload   |
| Sequence               | 8 bytes   | `00 00 00 00 00 00 00 2A`   | Monotonic sequence = 42             |
| OpType                 | 1 byte    | `00`                        | 0 = Put, 1 = Delete                 |
| KeyLen                 | 4 bytes   | `00 00 00 02`               | Length of key                       |
| Key bytes              | 2 bytes   | `61 62`                     | "ab"                                |
| ValueLen               | 4 bytes   | `00 00 00 03`               | Length of value                     |
| Value bytes            | 3 bytes   | `01 02 03`                  | Payload                             |


#### Step-by-step WAL record iteration (length+CRC framing):

  1. Open the WAL segment for reading from offset 0 (or resume from a known offset). Use buffered reads to reduce syscalls.
  2. Peek the length prefix (e.g., 4 or 8 bytes, big-endian). If EOF before full length is read, stop: treat remainder as truncated.
  3. Read the next 4 bytes as CRC32C. If EOF before CRC completes, stop.
  4. Read exactly RecordLength bytes as the payload. If short read, stop as truncated.
  5. Compute CRC32C over the payload and compare to the stored CRC. If it mismatches, treat this record and everything after it as invalid (likely torn write);
     stop iteration.
  6. Decode the payload fields in order: Sequence, OpType, KeyLen, Key bytes, ValueLen, Value bytes. Validate lengths (no negative/overflow; KeyLen/ValueLen
     within payload bounds).
  7. Yield the mutation (sequence, op, key, value) to the caller. Optionally assert monotonic Sequence (strictly increasing).
  8. Advance the read offset to the start of the next record and repeat from step 2 until EOF or a corrupt/truncated record is encountered.
  9. On recovery, ignore/truncate the trailing invalid portion; replay only the validated records in order into the memtable.

## CLI
* Provide a thin CLI (or admin subcommands) for `get/put/delete/scan`, `health`, and basic `stats` (WAL size, compaction backlog) over the same REST endpoints for smoke tests and operations.
