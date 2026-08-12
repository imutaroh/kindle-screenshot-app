package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// partNumRe は PDF ファイル名から part 番号を抜き出す。Python 版 src/server.py の _PART_NUM_RE と同じパターン。
var partNumRe = regexp.MustCompile(`(?i)part[_-]?(\d+)`)

// partSortKey は part 番号を数値として抽出し、part10 が part2 の後に来る文字列ソートのバグを避ける。
// マッチしたファイルは番号昇順、マッチしないファイルはファイル名順で末尾に回る。
type partSortKey struct {
	hasPart bool
	num     int
	name    string
}

func makePartSortKey(filename string) partSortKey {
	if m := partNumRe.FindStringSubmatch(filename); m != nil {
		n, _ := strconv.Atoi(m[1])
		return partSortKey{hasPart: true, num: n, name: filename}
	}
	return partSortKey{hasPart: false, name: filename}
}

func lessPartSortKey(a, b partSortKey) bool {
	if a.hasPart != b.hasPart {
		return a.hasPart // part番号ありが先
	}
	if a.hasPart {
		if a.num != b.num {
			return a.num < b.num
		}
	}
	return a.name < b.name
}

type bookPart struct {
	Filename string `json:"filename"`
	Pages    any    `json:"pages"`
	Size     int64  `json:"size"`
}

type book struct {
	Name      string     `json:"name"`
	Parts     []bookPart `json:"parts"`
	TotalSize int64      `json:"total_size"`
	MTime     float64    `json:"mtime"`
}

func (s *Server) handleBooks(w http.ResponseWriter, r *http.Request) {
	books := []book{}

	entries, err := os.ReadDir(s.outRoot)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			bookDir := filepath.Join(s.outRoot, entry.Name())

			files, err := os.ReadDir(bookDir)
			if err != nil {
				continue
			}

			var pdfFiles []os.DirEntry
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				if strings.EqualFold(filepath.Ext(f.Name()), ".pdf") {
					pdfFiles = append(pdfFiles, f)
				}
			}
			if len(pdfFiles) == 0 {
				continue
			}

			sort.Slice(pdfFiles, func(i, j int) bool {
				return lessPartSortKey(makePartSortKey(pdfFiles[i].Name()), makePartSortKey(pdfFiles[j].Name()))
			})

			parts := make([]bookPart, 0, len(pdfFiles))
			var totalSize int64
			for _, f := range pdfFiles {
				info, err := f.Info()
				if err != nil {
					continue
				}
				totalSize += info.Size()
				parts = append(parts, bookPart{Filename: f.Name(), Pages: nil, Size: info.Size()})
			}

			dirInfo, err := entry.Info()
			if err != nil {
				continue
			}

			books = append(books, book{
				Name:      entry.Name(),
				Parts:     parts,
				TotalSize: totalSize,
				MTime:     float64(dirInfo.ModTime().UnixNano()) / 1e9,
			})
		}
	}

	sort.SliceStable(books, func(i, j int) bool {
		return books[i].MTime > books[j].MTime
	})

	writeJSON(w, http.StatusOK, map[string]any{"books": books})
}

// handlePDF は本のPDFファイルを配信する（パストラバーサル対策済み、Range対応は http.ServeFile が自動で行う）。
func (s *Server) handlePDF(w http.ResponseWriter, r *http.Request) {
	bookName := r.PathValue("book")
	filename := r.PathValue("filename")

	if containsPathTraversal(bookName) || containsPathTraversal(filename) {
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(filepath.Ext(filename), ".pdf") {
		http.NotFound(w, r)
		return
	}

	path := filepath.Join(s.outRoot, bookName, filename)
	http.ServeFile(w, r, path)
}

func containsPathTraversal(s string) bool {
	return strings.Contains(s, "..") || strings.Contains(s, "/") || strings.Contains(s, "\\")
}
