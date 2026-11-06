package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

func sha1File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha1Bytes(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

func safeJoin(root, rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("плохой путь: %s", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", fmt.Errorf("плохой путь: %s", rel)
		}
	}
	dest := filepath.Join(root, filepath.FromSlash(rel))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	sep := string(os.PathSeparator)
	if absDest != absRoot && !strings.HasPrefix(absDest, absRoot+sep) {
		return "", fmt.Errorf("плохой путь: %s", rel)
	}
	return absDest, nil
}

func compareVer(a, b string) int {
	pa := splitVer(a)
	pb := splitVer(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		va, vb := 0, 0
		if i < len(pa) {
			va = pa[i]
		}
		if i < len(pb) {
			vb = pb[i]
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}

func splitVer(s string) []int {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '+' || r == '-' })
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		out = append(out, leadingInt(p))
	}
	return out
}

func leadingInt(p string) int {
	n := 0
	any := false
	for _, c := range p {
		if c < '0' || c > '9' {
			break
		}
		any = true
		n = n*10 + int(c-'0')
	}
	if !any {
		return 0
	}
	return n
}

func pool(limit, n int, worker func(i int) error) error {
	if n <= 0 {
		return nil
	}
	if limit < 1 {
		limit = 1
	}
	if limit > n {
		limit = n
	}
	jobs := make(chan int, n)
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	var failed atomic.Bool
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	for w := 0; w < limit; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if failed.Load() {
					return
				}
				if err := worker(i); err != nil {
					failed.Store(true)
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return first
}

func redact(text string) string {
	out := text
	for {
		i := strings.Index(out, "eyJ")
		if i < 0 {
			break
		}
		j := i + 3
		for j < len(out) {
			c := out[j]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
				j++
				continue
			}
			break
		}
		if j-i < 15 {
			break
		}
		out = out[:i] + "[token]" + out[j:]
	}
	if len(out) > 500 {
		out = out[:500]
	}
	return out
}
