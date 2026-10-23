package agent

// 사용자 대면 출력 언어 선택. 기본 프롬프트와 운영 지시문은 한국어로 제공하며,
// 모델이 작성하는 자연어 출력은 사용자가 선택한 표시 언어를 따릅니다.

// OutputLanguage 는 서버가 설정(config 의 ARTEX_LANG, 런타임 settings KV)에서 고른
// 출력 언어 코드("en"/"ko")를 돌려주도록 서버가 배선하는 훅이다. nil 이거나
// 빈 값이면 아래 기본값으로 떨어진다. PromptOverride 와 같은 방식으로 서버가 주입한다.
var OutputLanguage func() string

// 서버가 훅을 배선하지 않은 경우(예: 에이전트 패키지 단독 테스트)의 기본 출력 언어.
// 기본값은 한국어다(에이전트 패키지 단독 테스트의 폴백이며, 운영에서는 서버가 설정
// 계층의 기본값인 한국어로 OutputLanguage 훅을 배선한다). 영어는 설정으로 전환한다.
const defaultOutputLang = "ko"

// SupportedOutputLangs 는 전환할 수 있는 출력 언어 코드 목록이다.
var SupportedOutputLangs = []string{"ko", "en"}

// normalizeLang 은 지원 목록 밖의 코드를 기본값으로 좁힌다.
func normalizeLang(code string) string {
	for _, c := range SupportedOutputLangs {
		if c == code {
			return code
		}
	}
	return defaultOutputLang
}

// resolveOutputLang 은 현재 적용할 출력 언어 코드를 돌려준다. 서버가 배선한 훅을 먼저
// 보고, 없으면 기본값을 쓴다.
func resolveOutputLang() string {
	if OutputLanguage != nil {
		if code := OutputLanguage(); code != "" {
			return normalizeLang(code)
		}
	}
	return defaultOutputLang
}

// langPhrase는 출력 지시문에서 선택한 언어를 명확히 지정하는 이름입니다.
var langPhrase = map[string]string{
	"ko": "한국어",
	"en": "영어(English)",
}

// langBare 는 지시문 안에서 반복 지목할 때 쓰는 짧은 언어명이다.
var langBare = map[string]string{
	"ko": "한국어",
	"en": "영어",
}
