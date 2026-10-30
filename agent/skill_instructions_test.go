package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/skill"
)

func TestScopeSentrySkillLoadsArtexInstructions(t *testing.T) {
	registry, err := skill.LoadDir(filepath.Join("..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := registry.Get("scopesentry")
	if !ok {
		t.Fatal("기존 scopesentry 호출 이름으로 스킬을 찾을 수 없습니다")
	}
	if strings.TrimSpace(loaded.Description) == "" {
		t.Fatal("ScopeSentry 스킬의 검색 설명이 비어 있습니다")
	}
	if len(loaded.MCPs) != 1 || loaded.MCPs[0] != "ScopeSentry" {
		t.Fatalf("스킬이 실제 MCP 등록 이름과 연결되지 않습니다: %v", loaded.MCPs)
	}
	for _, section := range []string{"SearchExtraTools", "ExecuteExtraTool", "서비스 주소와 인증"} {
		if !strings.Contains(loaded.Instructions, section) {
			t.Fatalf("스킬을 읽는 과정에서 준비 지침이 누락됐습니다: %s", section)
		}
	}
	result, err := registry.Tool().Call(context.Background(), json.RawMessage(`{"name":"scopesentry"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Extra) != 1 || !strings.Contains(result.Extra[0].Text(), loaded.Instructions) {
		t.Fatal("Skill 호출이 준비 절을 포함한 본문 전체를 에이전트에게 전달하지 못했습니다")
	}
}
