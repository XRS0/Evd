package http

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func streamFile(w http.ResponseWriter, r *http.Request, fullPath, contentType string) {
	file, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fileSize := info.Size()
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, file)
		return
	}

	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); err != nil {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	end = fileSize - 1
	if strings.Contains(rangeHeader, "-") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if len(parts) == 2 && parts[1] != "" {
			_, _ = fmt.Sscanf(parts[1], "%d", &end)
		}
	}

	if start < 0 || start >= fileSize {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if end >= fileSize {
		end = fileSize - 1
	}
	if start > end {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	contentLength := end - start + 1
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = file.Seek(start, 0)
	_, _ = io.CopyN(w, file, contentLength)
}

func streamGrowingFile(w http.ResponseWriter, r *http.Request, fullPath, contentType string, done func() bool) {
	file, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)

	for {
		n, err := file.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}

		if err == io.EOF {
			if done != nil && done() {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return
		}
	}
}
