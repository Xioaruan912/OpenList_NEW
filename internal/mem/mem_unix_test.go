//go:build unix

package mem

import (
	"sync"
	"testing"
)

func TestMMapConcurrentReallocFree(t *testing.T) {
	for i := 0; i < 20; i++ {
		m, err := NewMemory(1024*1024, 16*1024*1024)
		if err != nil {
			t.Fatal(err)
		}
		mm := m.(*mmappedMemory)
		var wg sync.WaitGroup
		// 并发 Reallocate（模拟下载分片）
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					size := uint64((g*7+j)*4096%8*1024*1024) + 4096
					all, err := mm.Reallocate(size)
					if err == nil {
						_ = all
					}
				}
			}(g)
		}
		// 并发 Free
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mm.Free()
		}()
		wg.Wait()
		// 清理（Free 已延迟，直接 munmap 释放测试资源）
		_ = mm.realMunmap()
	}
}
