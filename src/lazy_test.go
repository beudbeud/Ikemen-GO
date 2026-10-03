package main

import (
	"runtime"
	"testing"
)

func TestLazyQueue(t *testing.T) {
	lazyQueue.q = nil
	t.Cleanup(func() { lazyQueue.q = nil })
	mk := func(g, n uint16) *Sprite {
		s := newSprite()
		s.Group, s.Number = g, n
		s.lazy = &lazyTex{data: []byte{1}}
		s.lazy.warm.Store(true)
		return s
	}
	keep := []*Sprite{mk(5000, 0), mk(0, 1), mk(0, 0)}
	lazyEnqueue(append([]*Sprite{mk(200, 0)}, keep...)) // the 200 one is dropped
	if got := lazyQueue.q[0].w.Value(); got != keep[2].lazy {
		t.Fatal("queue not ordered by group then number")
	}

	// Dropped sprites are skipped; the cap veto stops before making anything.
	runtime.GC()
	vetoed := 0
	lazyGPUOK = func() bool { vetoed++; return false }
	t.Cleanup(func() { lazyGPUOK = nil })
	if made, retry := lazyPrefetchOne(); made || retry || vetoed != 1 {
		t.Fatalf("veto: made %v retry %v, asked %d times", made, retry, vetoed)
	}
	runtime.KeepAlive(keep)
}
