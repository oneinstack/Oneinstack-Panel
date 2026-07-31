package filemanager

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	pathpkg "path"
	"strings"
	"time"
	"unicode"
)

const (
	defaultSearchLimit   = 100
	maxSearchLimit       = 500
	defaultSearchDepth   = 32
	maxSearchDepth       = 64
	maxSearchVisited     = 200000
	maxSearchQueryLength = 128
)

var errSearchStopped = errors.New("file search stopped")

type SearchOptions struct {
	Path       string
	Query      string
	Type       string
	MaxResults int
	MaxDepth   int
}

type SearchEntry struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	IsDir       bool      `json:"isDir"`
	IsSymlink   bool      `json:"isSymlink"`
	Size        int64     `json:"size"`
	Permissions string    `json:"permissions"`
	ModTime     time.Time `json:"modTime"`
}

type SearchResult struct {
	Items        []SearchEntry `json:"items"`
	Total        int           `json:"total"`
	Visited      int           `json:"visited"`
	Skipped      int           `json:"skipped"`
	Truncated    bool          `json:"truncated"`
	SearchedPath string        `json:"searchedPath"`
}

// Search finds files and directories by name without following directory
// symlinks. Searches from the server root skip volatile virtual filesystems;
// users can still browse or explicitly search those paths when necessary.
func (m *Manager) Search(ctx context.Context, options SearchOptions) (SearchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query := strings.TrimSpace(options.Query)
	if query == "" || len([]rune(query)) > maxSearchQueryLength {
		return SearchResult{}, fmt.Errorf("%w: search query must contain 1-%d characters", ErrInvalidPath, maxSearchQueryLength)
	}
	for _, value := range query {
		if unicode.IsControl(value) {
			return SearchResult{}, fmt.Errorf("%w: search query contains control characters", ErrInvalidPath)
		}
	}
	searchType := strings.ToLower(strings.TrimSpace(options.Type))
	if searchType == "" {
		searchType = "all"
	}
	if searchType != "all" && searchType != "file" && searchType != "dir" {
		return SearchResult{}, fmt.Errorf("%w: search type must be all, file, or dir", ErrInvalidPath)
	}
	limit := options.MaxResults
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	maxDepth := options.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultSearchDepth
	}
	if maxDepth > maxSearchDepth {
		maxDepth = maxSearchDepth
	}

	start, err := m.Relative(options.Path)
	if err != nil {
		return SearchResult{}, err
	}
	info, err := m.root.Stat(start)
	if err != nil {
		return SearchResult{}, err
	}
	if !info.IsDir() {
		return SearchResult{}, fmt.Errorf("%w: search path is not a directory", ErrInvalidPath)
	}

	result := SearchResult{
		Items:        make([]SearchEntry, 0, min(limit, defaultSearchLimit)),
		SearchedPath: m.VirtualPath(start),
	}
	lowerQuery := strings.ToLower(query)
	walkErr := fs.WalkDir(m.root.FS(), start, func(current string, entry fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			result.Truncated = true
			return errSearchStopped
		}
		if walkErr != nil {
			if current == start {
				return walkErr
			}
			result.Skipped++
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if current == start {
			return nil
		}
		if isInternalPath(current) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if start == "." && isVolatileSystemPath(current) {
			if entry.IsDir() {
				result.Skipped++
				return fs.SkipDir
			}
			return nil
		}
		if searchDepth(start, current) > maxDepth {
			if entry.IsDir() {
				result.Skipped++
				return fs.SkipDir
			}
			return nil
		}

		result.Visited++
		if result.Visited > maxSearchVisited {
			result.Truncated = true
			return errSearchStopped
		}
		isDir := entry.IsDir()
		if (searchType == "file" && isDir) || (searchType == "dir" && !isDir) ||
			!strings.Contains(strings.ToLower(entry.Name()), lowerQuery) {
			return nil
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			result.Skipped++
			return nil
		}
		result.Items = append(result.Items, SearchEntry{
			Path:        m.VirtualPath(current),
			Name:        entry.Name(),
			IsDir:       isDir,
			IsSymlink:   entry.Type()&fs.ModeSymlink != 0,
			Size:        fileInfo.Size(),
			Permissions: fmt.Sprintf("%04o", fileInfo.Mode().Perm()),
			ModTime:     fileInfo.ModTime().UTC(),
		})
		if len(result.Items) >= limit {
			result.Truncated = true
			return errSearchStopped
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errSearchStopped) {
		return SearchResult{}, walkErr
	}
	result.Total = len(result.Items)
	return result, nil
}

func searchDepth(start, current string) int {
	relative := strings.TrimPrefix(current, start)
	relative = strings.TrimPrefix(relative, "/")
	if relative == "" {
		return 0
	}
	return strings.Count(relative, "/") + 1
}

func isVolatileSystemPath(current string) bool {
	first := strings.SplitN(strings.TrimPrefix(pathpkg.Clean(current), "./"), "/", 2)[0]
	switch first {
	case "proc", "sys", "dev", "run":
		return true
	default:
		return false
	}
}
