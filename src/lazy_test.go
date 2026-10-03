package main

import (
	"runtime"
	"testing"
	"weak"
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
	lazyFull.Store(true)
	t.Cleanup(func() { lazyFull.Store(false) })
	if made, retry := lazyPrefetchOne(); made || retry {
		t.Fatalf("veto: made %v retry %v", made, retry)
	}
	runtime.KeepAlive(keep)
}

type fakeTex struct {
	Texture
	released bool
}

func (f *fakeTex) release() { f.released = true }

func TestLazyEvict(t *testing.T) {
	saved, savedFrame := lazyMade, sys.frameCounter
	t.Cleanup(func() { lazyMade, sys.frameCounter = saved, savedFrame })
	sys.frameCounter = 10000
	mk := func(used int32) (*lazyTex, *fakeTex) {
		f := &fakeTex{}
		l := &lazyTex{w: 10, h: 10, depth: 32, tex: f, used: used, made: true}
		lazyMade = append(lazyMade, weak.Make(l))
		return l, f
	}
	lazyMade = nil
	oldest, f1 := mk(100)
	older, f2 := mk(200)
	recent, f3 := mk(9900)           // drawn less than 10s ago: kept whatever the need
	if n := lazyEvict(1); n != 400 { // one 10x10x4 texture covers it
		t.Fatalf("freed %d bytes, want 400", n)
	}
	if !f1.released || oldest.tex != nil || f2.released || older.tex == nil {
		t.Fatalf("want only the least recently drawn evicted: %v %v", f1.released, f2.released)
	}
	lazyEvict(1 << 30)
	if !f2.released || f3.released || recent.tex == nil {
		t.Fatalf("recent texture evicted, or idle one kept: %v %v", f2.released, f3.released)
	}
}
