package adapter

type Adapter interface {
    Read() ([]byte, error)
    Close()
}