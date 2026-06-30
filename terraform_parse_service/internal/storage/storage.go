package storage

type Writer interface {
	Write(name string, content []byte) (string, error)
}
