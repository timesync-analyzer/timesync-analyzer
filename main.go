// test_zmq.go
package main

import (
	"fmt"
	"time"

	"github.com/pebbe/zmq4"
)

func main() {
    ctx, _ := zmq4.NewContext()
    socket, _ := ctx.NewSocket(zmq4.PULL)
    socket.SetRcvtimeo(100 * time.Millisecond)
    socket.Bind("tcp://*:10000")
    
    fmt.Println("Listening on tcp://*:10000")
    fmt.Println("NO other code running, just pure ZMQ")
    
    count := 0
    for {
        msg, err := socket.RecvBytes(0)
        if err != nil {
            continue
        }
        count++
        fmt.Printf("Message #%d: size=%d bytes\n", count, len(msg))
    }
}