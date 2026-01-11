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