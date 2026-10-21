package intercept

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"
)

func TestKoreanVerdictContract(t *testing.T) {
	reason := "실제 동작: 파일 읽기; 성공 후 결과: 내용을 반환함; 적용 규칙: A5"
	for _, action := range []string{"allow", "ask", "deny"} {
		raw, _ := json.Marshal(map[string]string{"decision": action, "comment": reason})
		got := ParseVerdict(string(raw))
		if got.Action != action || got.Reason != reason {
			t.Fatalf("Korean verdict lost: %+v", got)
		}
	}
	for _, invalid := range []string{
		strings.Replace(reason, "파일 읽기", "", 1), strings.Replace(reason, "내용을 반환함", "", 1), strings.Replace(reason, "A5", "", 1),
		strings.Replace(reason, "; 성공 후 결과: ", "；成功后的后果：", 1),
		strings.Replace(reason, "; 적용 규칙: ", "；命中规则：", 1),
		strings.Repeat(reason, 100),
	} {
		raw, _ := json.Marshal(map[string]string{"decision": "allow", "comment": invalid})
		if got := ParseVerdict(string(raw)); got.Action != "" {
			t.Fatalf("invalid Korean verdict accepted: %+v", got)
		}
	}
	for _, prompt := range []string{DefaultJudgePrompt, JudgeContextBoundary, JudgeOutputContract} {
		for _, r := range prompt {
			if unicode.Is(unicode.Han, r) {
				t.Fatal("default judge prompt still contains Chinese")
			}
		}
	}
}

func TestLegacyDefaultJudgePromptRecognition(t *testing.T) {
	old, err := os.ReadFile("testdata/legacy_judge_prompt.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !isLegacyDefaultJudgePrompt(string(old)) || !isLegacyDefaultJudgePrompt("\n"+string(old)+"\n") {
		t.Fatal("legacy built-in template not recognized")
	}
	if isLegacyDefaultJudgePrompt(string(old)+"\nCustom policy") || isLegacyDefaultJudgePrompt(DefaultJudgePrompt) {
		t.Fatal("custom/current prompt mistaken for legacy")
	}
}
