package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepOnlyMatchingLongLine(t *testing.T) {
	for _, backend := range []string{"go", "ripgrep"} {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("NORMA_RIPGREP_NO_INSTALL", "1")
			t.Setenv("NORMA_DISABLE_RIPGREP", "1")
			if backend == "ripgrep" {
				if _, err := exec.LookPath("rg"); err != nil {
					t.Skip("rg가 설치되지 않아 ripgrep 검색을 검사하지 못함")
				}
				t.Setenv("NORMA_DISABLE_RIPGREP", "")
			}
			dir := t.TempDir()
			const middle = "left1 /rest/user/delete right1"
			const end = "left2 /rest/user/delete right2"
			source := strings.Repeat("x", 600_000) + middle + strings.Repeat("y", 600_000) + end
			path := filepath.Join(dir, "main.js")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name      string
				offset    int
				limit     int
				showLines bool
				want      string
			}{
				{"middle and end", 0, 0, true, "main.js:1:" + middle + "\nmain.js:1:" + end + "\n"},
				{"first page", 0, 1, true, "main.js:1:" + middle + "\n... [1 more result(s) — page with offset=1, or head_limit=0 for all] ...\n"},
				{"next page", 1, 1, true, "main.js:1:" + end + "\n"},
				{"past last page", 2, 1, true, ""},
				{"without line numbers", 0, 0, false, "main.js:" + middle + "\nmain.js:" + end + "\n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					result := run(t, NewGrep(), dir, map[string]any{
						"path": "main.js", "pattern": `.{0,6}/rest/user/delete.{0,7}`,
						"output_mode": "content", "only_matching": true,
						"offset": tc.offset, "head_limit": tc.limit, "-n": tc.showLines,
					})
					if result.IsError || result.Flatten() != tc.want {
						t.Fatalf("일치 부분 출력=%.200q, 원하는 값=%q", result.Flatten(), tc.want)
					}
					if backend == "ripgrep" {
						direct, ok := runWithRipgrep(RipgrepPath(), ripgrepReq{
							root: path, pattern: `.{0,6}/rest/user/delete.{0,7}`, mode: "content", onlyMatching: true,
							showLines: tc.showLines, offset: tc.offset, headLimit: &tc.limit,
						}, &ToolContext{WorkingDir: dir})
						if !ok || direct.Flatten() != tc.want {
							t.Fatalf("ripgrep 실행이 fallback 없이 성공하지 못함: ok=%v 결과=%.200q", ok, direct.Flatten())
						}
					}
				})
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != source {
				t.Fatalf("검색이 원본 파일을 변경함: %v", err)
			}
		})
	}
}

func TestGrepOnlyMatchingKeepsOutputModes(t *testing.T) {
	for _, backend := range []string{"go", "ripgrep"} {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("NORMA_RIPGREP_NO_INSTALL", "1")
			t.Setenv("NORMA_DISABLE_RIPGREP", "1")
			if backend == "ripgrep" {
				if _, err := exec.LookPath("rg"); err != nil {
					t.Skip("rg가 설치되지 않아 ripgrep 검색을 검사하지 못함")
				}
				t.Setenv("NORMA_DISABLE_RIPGREP", "")
			}
			dir := t.TempDir()
			for _, name := range []string{"z.js", "a.js"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("before\n한글 hit hit\nalpha\nbeta\nafter\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []string{"files_with_matches", "count"} {
				input := map[string]any{"path": "a.js", "pattern": "hit", "output_mode": mode}
				before := run(t, NewGrep(), dir, input).Flatten()
				input["only_matching"] = true
				if after := run(t, NewGrep(), dir, input).Flatten(); after != before {
					t.Fatalf("%s 출력 변경: %q / %q", mode, before, after)
				}
			}
			input := map[string]any{"path": "a.js", "pattern": "hit", "output_mode": "content", "-C": 1}
			before := run(t, NewGrep(), dir, input).Flatten()
			input["only_matching"] = false
			if after := run(t, NewGrep(), dir, input).Flatten(); after != before || !strings.Contains(after, "before") {
				t.Fatalf("기본 전체 행/문맥 출력 변경: %q / %q", before, after)
			}
			input["only_matching"] = true
			delete(input, "path")
			if got := run(t, NewGrep(), dir, input).Flatten(); got != "a.js:2:hit\na.js:2:hit\nz.js:2:hit\nz.js:2:hit\n" {
				t.Fatalf("전체 문맥 제외 또는 파일순 출력 실패: %q", got)
			}
			input["path"], input["multiline"], input["pattern"] = "a.js", true, `alpha\nbeta`
			if got := run(t, NewGrep(), dir, input).Flatten(); got != "a.js:3:alpha\na.js:4:beta\n" {
				t.Fatalf("여러 행 일치 출력 실패: %q", got)
			}
			if err := os.WriteFile(filepath.Join(dir, "zero.txt"), []byte("abc\nb\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				pattern   string
				multiline bool
				want      string
			}{
				{"^", false, "zero.txt:1:\nzero.txt:2:\n"},
				{"$", false, "zero.txt:1:\nzero.txt:2:\n"},
				{"a*", false, "zero.txt:1:a\nzero.txt:1:\nzero.txt:1:\nzero.txt:2:\nzero.txt:2:\n"},
			} {
				input["path"], input["pattern"], input["multiline"] = "zero.txt", tc.pattern, tc.multiline
				if got := run(t, NewGrep(), dir, input).Flatten(); got != tc.want {
					t.Fatalf("빈 일치 출력 불일치: pattern=%q multiline=%v 결과=%q 원하는 값=%q", tc.pattern, tc.multiline, got, tc.want)
				}
			}
		})
	}
}
