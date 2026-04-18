package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const repoAPI = "https://api.github.com/repos/mugiwaraluffy56/wert/releases/latest"

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func LatestRelease() (*Release, error) {
	req, err := http.NewRequest(http.MethodGet, repoAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github API returned %d", resp.StatusCode)
	}
	var r Release
	return &r, json.NewDecoder(resp.Body).Decode(&r)
}

// AssetName returns the expected release asset name for the current platform.
func AssetName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("wert-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)
}

// Update downloads the latest release, replaces the current binary, and returns
// the path to the new executable so the caller can re-exec it.
// Progress lines are written to progressOut (pass os.Stdout for terminal output).
func Update(progressOut io.Writer) (execPath string, tag string, err error) {
	fmt.Fprintln(progressOut, "  fetching latest release...")
	rel, err := LatestRelease()
	if err != nil {
		return "", "", fmt.Errorf("fetch release: %w", err)
	}
	tag = rel.TagName

	assetName := AssetName()
	var downloadURL string
	var assetSize int64
	for _, a := range rel.Assets {
		if a.Name == assetName {
			downloadURL = a.BrowserDownloadURL
			assetSize = a.Size
			break
		}
	}
	if downloadURL == "" {
		return "", tag, fmt.Errorf("no asset for %s in release %s", assetName, tag)
	}

	fmt.Fprintf(progressOut, "  latest: %s — downloading %s", tag, assetName)
	if assetSize > 0 {
		fmt.Fprintf(progressOut, " (%s)", humanSize(assetSize))
	}
	fmt.Fprintln(progressOut)

	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", tag, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	execPath, err = os.Executable()
	if err != nil {
		return "", tag, fmt.Errorf("find executable: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", tag, fmt.Errorf("resolve symlinks: %w", err)
	}

	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, "wert-update-*")
	if err != nil {
		return "", tag, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				tmp.Close()
				os.Remove(tmpPath)
				return "", tag, fmt.Errorf("write: %w", werr)
			}
			written += int64(n)
			if assetSize > 0 {
				pct := written * 100 / assetSize
				fmt.Fprintf(progressOut, "\r  downloading... %d%%  (%s)", pct, humanSize(written))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return "", tag, fmt.Errorf("read: %w", rerr)
		}
	}
	tmp.Close()
	fmt.Fprintln(progressOut)

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return "", tag, fmt.Errorf("chmod: %w", err)
	}

	// On Windows we can't overwrite a running .exe directly —
	// rename the old binary out of the way first.
	if runtime.GOOS == "windows" {
		old := execPath + ".old"
		_ = os.Remove(old)
		if err := os.Rename(execPath, old); err != nil {
			os.Remove(tmpPath)
			return "", tag, fmt.Errorf("rename current binary: %w", err)
		}
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		os.Remove(tmpPath)
		return "", tag, fmt.Errorf("replace binary: %w", err)
	}

	return execPath, tag, nil
}

func humanSize(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
