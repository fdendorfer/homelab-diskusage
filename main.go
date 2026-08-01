package main

import (
	"embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed static/index.html
var staticFS embed.FS

type Node struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Children []*Node `json:"children,omitempty"`
}

type rootInfo struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

type scanState struct {
	mu       sync.Mutex
	scanning bool
	data     *Node
	lastScan time.Time
	err      string
}

var (
	roots  []rootInfo
	states = map[string]*scanState{}
	sem    = make(chan struct{}, runtime.NumCPU()*8)
)

func loadRoots() {
	spec := os.Getenv("ROOTS")
	if spec == "" {
		spec = "root:/data"
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			log.Printf("skipping malformed ROOTS entry: %q", part)
			continue
		}
		label, path := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if _, err := os.Stat(path); err != nil {
			log.Printf("root %q (%s) not accessible: %v", label, path, err)
			continue
		}
		roots = append(roots, rootInfo{Label: label, Path: path})
		states[label] = &scanState{}
	}
	if len(roots) == 0 {
		log.Fatal("no usable roots configured (set ROOTS=label:/path,label2:/path2)")
	}
}

func readDirLimited(path string) []os.DirEntry {
	// Only the syscall itself is rate-limited. A permit must never be held
	// across a recursive scanDir()+wg.Wait() call below, or a wide/deep tree
	// livelocks: parent goroutines block on wg.Wait() while holding a permit,
	// starving the children that need a permit to make any progress at all.
	sem <- struct{}{}
	defer func() { <-sem }()
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	return entries
}

// deviceOf returns the filesystem device ID a path lives on, so scanning can
// stay on one filesystem (like `du -x`). Bind-mounting host "/" into the
// container also exposes /proc, /sys, and — critically — Docker's own
// overlay "merged" mounts, which reflect the container's own live view of
// itself; without this check those recurse into nonsense sizes (a /proc
// entry alone reported as ~140TB from a stale kcore-style virtual file).
func deviceOf(path string) (uint64, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

func scanDir(path string, rootDev uint64) *Node {
	name := filepath.Base(path)
	node := &Node{Name: name}

	entries := readDirLimited(path)

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, e := range entries {
		e := e
		childPath := filepath.Join(path, e.Name())

		if e.IsDir() {
			if dev, ok := deviceOf(childPath); ok && dev != rootDev {
				// Different filesystem (mount point) — record it as a
				// boundary marker but don't descend into it.
				mu.Lock()
				node.Children = append(node.Children, &Node{Name: e.Name(), Size: 0})
				mu.Unlock()
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				child := scanDir(childPath, rootDev)
				mu.Lock()
				node.Children = append(node.Children, child)
				node.Size += child.Size
				mu.Unlock()
			}()
			continue
		}

		info, err := e.Info()
		var sz int64
		if err == nil {
			sz = info.Size()
		}
		mu.Lock()
		node.Children = append(node.Children, &Node{Name: e.Name(), Size: sz})
		node.Size += sz
		mu.Unlock()
	}

	wg.Wait()
	sort.Slice(node.Children, func(i, j int) bool {
		return node.Children[i].Size > node.Children[j].Size
	})
	return node
}

func findRoot(label string) (rootInfo, bool) {
	for _, r := range roots {
		if r.Label == label {
			return r, true
		}
	}
	return rootInfo{}, false
}

func handleRoots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, roots)
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("root")
	root, ok := findRoot(label)
	if !ok {
		http.Error(w, "unknown root", http.StatusBadRequest)
		return
	}
	st := states[label]

	st.mu.Lock()
	if st.scanning {
		st.mu.Unlock()
		writeJSON(w, map[string]string{"status": "already-running"})
		return
	}
	st.scanning = true
	st.err = ""
	st.mu.Unlock()

	go func() {
		rootDev, _ := deviceOf(root.Path)
		result := scanDir(root.Path, rootDev)
		st.mu.Lock()
		st.data = result
		st.lastScan = time.Now()
		st.scanning = false
		st.mu.Unlock()
	}()

	writeJSON(w, map[string]string{"status": "started"})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("root")
	st, ok := states[label]
	if !ok {
		http.Error(w, "unknown root", http.StatusBadRequest)
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	resp := map[string]interface{}{
		"scanning": st.scanning,
		"hasData":  st.data != nil,
		"error":    st.err,
	}
	if !st.lastScan.IsZero() {
		resp["lastScan"] = st.lastScan.Format(time.RFC3339)
	}
	writeJSON(w, resp)
}

func handleData(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("root")
	st, ok := states[label]
	if !ok {
		http.Error(w, "unknown root", http.StatusBadRequest)
		return
	}
	st.mu.Lock()
	data := st.data
	st.mu.Unlock()

	if data == nil {
		http.Error(w, "no scan data yet", http.StatusNotFound)
		return
	}
	writeJSON(w, data)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	loadRoots()

	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
	mux.HandleFunc("/api/roots", handleRoots)
	mux.HandleFunc("/api/scan", handleScan)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/data", handleData)

	log.Printf("diskusage listening on :8080, roots: %v", roots)
	log.Fatal(http.ListenAndServe(":8080", mux))
}
