package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func downloadFile(url, dest, sha string, hooks hook, label string) error {
	if !strings.HasPrefix(strings.ToLower(url), "https://") {
		return fmt.Errorf("нужна https-ссылка")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if sha != "" {
		if got, err := sha1File(dest); err == nil && strings.EqualFold(got, sha) {
			return nil
		}
	} else if st, err := os.Stat(dest); err == nil && st.Size() > 0 && !st.IsDir() {
		return nil
	}

	var last error
	for attempt := 1; attempt <= 4; attempt++ {
		if hooks.cancelled() {
			return fmt.Errorf("отменено")
		}
		err := downloadOnce(url, dest, sha, hooks, label)
		if err == nil {
			return nil
		}
		last = err
		if strings.Contains(err.Error(), "отменено") || attempt == 4 {
			break
		}
		time.Sleep(time.Duration(attempt) * 700 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("не скачалось")
	}
	return last
}

type piece struct {
	b   []byte
	err error
}

func downloadOnce(url, dest, sha string, hooks hook, label string) error {
	part := dest + ".part"
	_ = os.Remove(part)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "TBSLauncher/"+launcherVersion)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d для %s", resp.StatusCode, filepath.Base(dest))
	}
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(part)
		return err
	}
	total := resp.ContentLength
	var got int64
	chunks := make(chan piece)
	go func() {
		buf := make([]byte, 256*1024)
		for {
			n, err := resp.Body.Read(buf)
			var b []byte
			if n > 0 {
				b = append([]byte(nil), buf[:n]...)
			}
			chunks <- piece{b, err}
			if err != nil {
				return
			}
		}
	}()
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		if hooks.cancelled() {
			_ = resp.Body.Close()
			return fail(fmt.Errorf("отменено"))
		}
		select {
		case c := <-chunks:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(90 * time.Second)
			if len(c.b) > 0 {
				if _, err := f.Write(c.b); err != nil {
					_ = resp.Body.Close()
					return fail(err)
				}
				got += int64(len(c.b))
				if label != "" {
					hooks.progress(progress{Phase: label, Current: got, Total: total, Label: filepath.Base(dest)})
				}
			}
			if c.err == io.EOF {
				goto done
			}
			if c.err != nil {
				return fail(c.err)
			}
		case <-timer.C:
			_ = resp.Body.Close()
			return fail(fmt.Errorf("скачивание зависло"))
		}
	}
done:
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	if sha != "" {
		gotHash, err := sha1File(part)
		if err != nil {
			_ = os.Remove(part)
			return err
		}
		if !strings.EqualFold(gotHash, sha) {
			_ = os.Remove(part)
			return fmt.Errorf("битый файл %s", filepath.Base(dest))
		}
	}
	_ = os.Remove(dest)
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}
