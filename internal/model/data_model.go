package model

type OpsType byte

const (
	PUT OpsType = iota
	DELETE
)

type Mutation struct {
	Op    OpsType
	Key   []byte
	Value []byte
}
