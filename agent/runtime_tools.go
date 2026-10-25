package agent

import (
	"os/exec"
	"strings"
)

func workerRuntimeTools() string {
	var installed []string
	for _, name := range []string{"curl", "python3", "node", "jq", "nmap", "playwright-cli"} {
		if _, err := exec.LookPath(name); err == nil {
			installed = append(installed, name)
		}
	}
	return "\n\n**현재 실행 환경**: PATH에서 확인한 명령: " + strings.Join(installed, ", ") + ". 별도 Python 패키지는 설치되어 있다고 가정하지 마세요. 기본 HTTP 처리는 curl 또는 Python 표준 라이브러리 urllib.request를 사용할 수 있습니다. MCP는 실제 조회된 스키마의 필수 인자를 확인하고, 인자 오류 때 다른 URL·요청 index를 추측하지 마세요."
}
