//go:build unix

package mem

import (
	"math"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func NewMemory(cap, max uint64) (LinearMemory, error) {
	// Round up to the page size.
	rnd := uint64(unix.Getpagesize() - 1)
	res := (max + rnd) &^ rnd

	if res > math.MaxInt {
		// This ensures int(res) overflows to a negative value,
		// and unix.Mmap returns EINVAL.
		res = math.MaxUint64
	}

	com := res
	prot := unix.PROT_READ | unix.PROT_WRITE
	if cap < max { // Commit memory only if cap=max.
		com = 0
		prot = unix.PROT_NONE
	}

	// Reserve res bytes of address space, to ensure we won't need to move it.
	// A protected, private, anonymous mapping should not commit memory.
	b, err := unix.Mmap(-1, 0, int(res), prot, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		return nil, err
	}
	return &mmappedMemory{buf: b[:com]}, nil
}

// mmapFreeGrace Free 后延迟 Munmap 的宽限期：调用方可能仍有 goroutine 在使用
// Reallocate 返回的切片（如并发下载分片写入），立即 Munmap 会访问已解映射内存 → SEGV。
const mmapFreeGrace = 5 * time.Minute

var (
	freeMu   sync.Mutex
	freeList []*mmappedMemory
)

func init() {
	go func() {
		for {
			time.Sleep(mmapFreeGrace)
			sweepFreedMmaps()
		}
	}()
}

// sweepFreedMmaps 将超过宽限期的 mmap 真正 Munmap 并释放地址空间
func sweepFreedMmaps() {
	now := time.Now()
	freeMu.Lock()
	var ready []*mmappedMemory
	kept := freeList[:0]
	for _, m := range freeList {
		if now.Sub(m.freedAt) >= mmapFreeGrace {
			ready = append(ready, m)
		} else {
			kept = append(kept, m)
		}
	}
	freeList = kept
	freeMu.Unlock()
	// 释放 freeMu 后再 Munmap，避免与 Free（持 m.mu 再取 freeMu）形成锁序死锁
	for _, m := range ready {
		_ = m.realMunmap()
	}
}

// The slice covers the entire mmapped memory:
//   - len(buf) is the already committed memory,
//   - cap(buf) is the reserved address space.
type mmappedMemory struct {
	mu        sync.Mutex
	buf       []byte
	growCheck GrowCheck
	freedAt   time.Time
}

func (m *mmappedMemory) SetGrowCheck(c GrowCheck) {
	m.growCheck = c
}

func (m *mmappedMemory) Reallocate(size uint64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buf == nil {
		// 已标记释放（延迟 Munmap 中），不再分配
		return nil, ErrNotEnoughMemory
	}
	com := uint64(len(m.buf))
	res := uint64(cap(m.buf))
	if com < size {
		if size <= res {
			// Grow geometrically, round up to the page size.
			rnd := uint64(unix.Getpagesize() - 1)
			new := com + com>>3
			new = min(max(size, new), res)
			new = (new + rnd) &^ rnd

			if m.growCheck != nil {
				if err := m.growCheck(new - com); err != nil {
					return nil, err
				}
			}

			// Commit additional memory up to new bytes.
			err := unix.Mprotect(m.buf[com:new], unix.PROT_READ|unix.PROT_WRITE)
			if err != nil {
				return nil, err
			}

			m.buf = m.buf[:new] // Update committed memory.
		} else {
			return nil, ErrNotEnoughMemory
		}
	}
	// Limit returned capacity because bytes beyond
	// len(m.buf) have not yet been committed.
	return m.buf[:size:len(m.buf)], nil
}

// Free 延迟释放：把映射放入待释放队列，等宽限期后再 Munmap。
// 立即 Munmap 会与仍在读写该映射的 goroutine 竞争（use-after-free）导致进程 SEGV 崩溃。
func (m *mmappedMemory) Free() error {
	m.mu.Lock()
	if m.buf == nil {
		m.mu.Unlock()
		return nil
	}
	m.freedAt = time.Now()
	freeMu.Lock()
	freeList = append(freeList, m)
	freeMu.Unlock()
	m.mu.Unlock()
	return nil
}

// realMunmap 真正释放映射（仅由后台清理在宽限期后调用）
func (m *mmappedMemory) realMunmap() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buf != nil {
		err := unix.Munmap(m.buf[:cap(m.buf)])
		m.buf = nil
		return err
	}
	return nil
}
