// Package pipe copies bytes both ways between two connections.
package pipe

import (
	"io"
	"net"
	"sync"
)

type closeWriter interface{ CloseWrite() error }

// Bidirectional copies a→b and b→a until both directions end, propagating
// half-closes when the destination supports them, then closes both ends.
// It returns the bytes copied in each direction.
func Bidirectional(a, b net.Conn) (aToB, bToA int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		aToB = copyHalf(b, a)
	}()
	go func() {
		defer wg.Done()
		bToA = copyHalf(a, b)
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
	return aToB, bToA
}

func copyHalf(dst, src net.Conn) int64 {
	n, _ := io.Copy(dst, src)
	if cw, ok := dst.(closeWriter); ok {
		_ = cw.CloseWrite()
	} else {
		_ = dst.Close()
	}
	return n
}
