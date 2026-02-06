package transmission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	domainmedia "evd/internal/domain/media"
	"evd/internal/domain/torrent"
	"evd/internal/infrastructure/filesystem"
)

// Client is a Transmission RPC infrastructure adapter.
type Client struct {
	URL         string
	User        string
	Pass        string
	DownloadDir string
	HTTP        *http.Client
	mu          sync.Mutex
	sessionID   string
	store       *filesystem.Store
}

// NewClient creates a Transmission RPC adapter.
func NewClient(url, user, pass, downloadDir string, store *filesystem.Store) *Client {
	return &Client{
		URL:         strings.TrimSpace(url),
		User:        user,
		Pass:        pass,
		DownloadDir: downloadDir,
		HTTP:        &http.Client{Timeout: 12 * time.Second},
		store:       store,
	}
}

// Enabled reports whether Transmission integration is configured.
func (c *Client) Enabled() bool {
	return c.URL != ""
}

// List fetches torrent list and maps it into domain objects.
func (c *Client) List() ([]torrent.Info, error) {
	resp, err := c.request("torrent-get", map[string]interface{}{
		"fields": []string{
			"id",
			"name",
			"status",
			"percentDone",
			"rateDownload",
			"eta",
			"sizeWhenDone",
			"downloadedEver",
			"addedDate",
			"isFinished",
			"files",
		},
	})
	if err != nil {
		return nil, err
	}

	var args struct {
		Torrents []struct {
			ID             int     `json:"id"`
			Name           string  `json:"name"`
			Status         int     `json:"status"`
			PercentDone    float64 `json:"percentDone"`
			RateDownload   int64   `json:"rateDownload"`
			ETA            int     `json:"eta"`
			SizeWhenDone   int64   `json:"sizeWhenDone"`
			DownloadedEver int64   `json:"downloadedEver"`
			AddedDate      int64   `json:"addedDate"`
			IsFinished     bool    `json:"isFinished"`
			Files          []struct {
				BytesCompleted int64  `json:"bytesCompleted"`
				Length         int64  `json:"length"`
				Name           string `json:"name"`
			} `json:"files"`
		} `json:"torrents"`
	}
	if err := json.Unmarshal(resp.Arguments, &args); err != nil {
		return nil, err
	}

	items := make([]torrent.Info, 0, len(args.Torrents))
	for _, t := range args.Torrents {
		progress := int(t.PercentDone*100 + 0.5)
		files := make([]torrent.File, 0, len(t.Files))
		for idx, f := range t.Files {
			rel, err := domainmedia.NormalizeVideoPath(f.Name)
			if err != nil {
				continue
			}
			fileProgress := 0
			if f.Length > 0 {
				fileProgress = int(float64(f.BytesCompleted)/float64(f.Length)*100 + 0.5)
			}
			files = append(files, torrent.File{
				Index:          idx,
				Name:           f.Name,
				Path:           rel,
				Size:           f.Length,
				BytesCompleted: f.BytesCompleted,
				Progress:       fileProgress,
				Streamable:     f.BytesCompleted > 0 && c.store.FileExists(rel),
			})
		}
		items = append(items, torrent.Info{
			ID:             t.ID,
			Name:           t.Name,
			Status:         mapStatus(t.Status),
			PercentDone:    t.PercentDone,
			Progress:       progress,
			RateDownload:   t.RateDownload,
			ETA:            t.ETA,
			SizeWhenDone:   t.SizeWhenDone,
			DownloadedEver: t.DownloadedEver,
			AddedDate:      t.AddedDate,
			IsFinished:     t.IsFinished,
			Files:          files,
		})
	}

	return items, nil
}

// AddTorrent adds torrent metadata to Transmission.
func (c *Client) AddTorrent(metainfo string) error {
	_, err := c.request("torrent-add", map[string]interface{}{
		"metainfo":     metainfo,
		"download-dir": c.DownloadDir,
		"paused":       false,
	})
	return err
}

// SetSequentialDownload toggles sequential mode for a torrent.
func (c *Client) SetSequentialDownload(id int, enabled bool) error {
	_, err := c.request("torrent-set", map[string]interface{}{
		"ids":                []int{id},
		"sequentialDownload": enabled,
	})
	return err
}

type response struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c *Client) request(method string, arguments map[string]interface{}) (response, error) {
	if !c.Enabled() {
		return response{}, errors.New("Transmission is not configured")
	}

	payload := map[string]interface{}{
		"method":    method,
		"arguments": arguments,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest("POST", c.URL, bytes.NewReader(data))
		if err != nil {
			return response{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		if sessionID := c.getSessionID(); sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", sessionID)
		}
		if c.User != "" || c.Pass != "" {
			req.SetBasicAuth(c.User, c.Pass)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return response{}, err
		}

		if resp.StatusCode == http.StatusConflict {
			newID := resp.Header.Get("X-Transmission-Session-Id")
			resp.Body.Close()
			if newID == "" {
				return response{}, errors.New("Transmission session id missing")
			}
			c.setSessionID(newID)
			continue
		}

		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return response{}, fmt.Errorf("Transmission error: %s", strings.TrimSpace(string(body)))
		}

		var out response
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return response{}, err
		}
		if out.Result != "success" {
			return response{}, fmt.Errorf("Transmission error: %s", out.Result)
		}
		return out, nil
	}

	return response{}, errors.New("Transmission session negotiation failed")
}

func mapStatus(code int) string {
	switch code {
	case 0:
		return "stopped"
	case 1:
		return "check_wait"
	case 2:
		return "checking"
	case 3:
		return "download_wait"
	case 4:
		return "downloading"
	case 5:
		return "seed_wait"
	case 6:
		return "seeding"
	default:
		return "unknown"
	}
}

func (c *Client) getSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *Client) setSessionID(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = value
}
