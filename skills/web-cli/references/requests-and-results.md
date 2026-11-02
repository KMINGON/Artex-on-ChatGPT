# HTTP 요청 재사용과 결과 해석

예시의 `<대상>`, `<쿠키>`, `<인자>`는 현재 작업에서 확보한 값으로 바꿉니다. 명령은 현재 작업 디렉터리 또는 지정된 `intent_dir`에서 실행하며 설치된 `ffuf -h`, `sqlmap -hh`의 옵션을 기준으로 합니다.

## ffuf: 인증한 요청에서 후보 비교

브라우저나 API 조사에서 확보한 메서드·헤더·본문을 raw HTTP 요청 파일로 저장하고, 바꿔 볼 위치에 `FUZZ`를 넣습니다. 예를 들어 `request-ffuf.txt`는 다음 형태입니다. 헤더와 본문 사이의 빈 줄을 유지합니다.

```http
GET /api/FUZZ HTTP/1.1
Host: <대상>
Cookie: session=<쿠키>
Accept: application/json

```

```bash
ffuf -request ./request-ffuf.txt -request-proto https \
  -w /usr/share/seclists/Discovery/Web-Content/common-api-endpoints-mazen160.txt \
  -mc all -of json -o ./api-paths.json
```

이 예시는 상태 코드에 따른 초기 제외 없이 응답을 모읍니다. 기준 응답과 후보를 비교해 반복되는 오류 페이지·로그인 화면을 분리합니다. 정상 경로와 같은 길이의 응답도 있을 수 있으므로 필터가 숨기는 결과를 해석에 포함합니다.

| 비교할 관찰 | 관련 옵션 |
| --- | --- |
| HTTP 상태 코드 | `-mc` 포함 / `-fc` 제외 |
| 응답 크기 | `-fs` |
| 단어 수·줄 수 | `-fw`, `-fl` |
| 반복되는 오류 문구 | `-fr` 정규식 |

URL 인자 값 후보는 URL의 해당 위치에, 인자 이름 후보는 이름 위치에 `FUZZ`를 둡니다. 가상 호스트를 비교할 때는 실제 연결할 URL과 `Host` 헤더의 후보를 구분합니다. 인증 리다이렉트가 반복되면 사전 크기를 늘리기 전에 쿠키 유효성·호스트·경로가 기존 요청과 같은지 확인합니다.

## sqlmap: 확인할 인자를 지정한 요청

정상 요청 원문을 `request-sqlmap.txt`로 저장합니다. ffuf용 `FUZZ`를 그대로 넘기지 말고 현재 요청의 실제 값을 사용합니다.

```http
GET /api/item?id=1 HTTP/1.1
Host: <대상>
Cookie: session=<쿠키>

```

```bash
sqlmap -r ./request-sqlmap.txt -p id --batch --force-ssl \
  --output-dir=./sqlmap-item
```

위 예시는 HTTPS 요청입니다. HTTP 대상에는 `--force-ssl`을 적용하지 않습니다. POST·JSON 요청은 해당 메서드, Content-Type, 본문을 유지합니다. 쿠키·CSRF가 갱신되는 요청은 `sqlmap -hh`에서 `--csrf-token`, `--csrf-url` 등 현재 요청에 필요한 지원을 확인합니다. 무조건 옵션을 추가하거나 요청 강도를 고정하지 말고 실제 동작과 작업 지침에 맞춥니다.

`--batch`는 대화식 기본 답변을 선택하는 옵션이며 요청의 적합성이나 검증 성공을 뜻하지 않습니다. 예전 세션을 재사용한 출력은 현재 요청에서 새로 관측한 결과와 구분합니다.

## 프록시와 실패 해석

ffuf에는 `-x`, sqlmap에는 `--proxy`를 사용할 수 있습니다. 현재 실행 환경이 전달한 실제 프록시 주소를 확인해 적용합니다. Nmap의 일반 포트 스캔은 HTTP 프록시로 라우팅되는 웹 요청과 다릅니다. CLI 출력을 저장하는 것과 ARTEX가 HTTP 트래픽을 기록하는 것은 별개입니다.

| 출력·현상 | 다음에 볼 정보 |
| --- | --- |
| DNS 오류·연결 거부·시간 초과 | 대상 주소·네트워크·실제 서비스 포트 |
| 401/403 또는 로그인 리다이렉트 반복 | 인증 세션·Host·권한·기준 요청 |
| 동일한 200 응답이 대부분의 후보에 나타남 | 본문과 크기·단어·줄 수, 공통 오류 페이지 여부 |
| 필터 적용 후 후보 없음 | 적용한 필터와 원문. 전체 경로 부재로 단정하지 않음 |
| 일부 대상만 처리한 뒤 중단 | 처리한 대상·실패 목록·기존 결과 파일. 부분 결과로 전달 |

원문에는 인증 정보가 들어갈 수 있으므로 결과 인계에는 관찰 요약과 파일의 절대 경로를 사용합니다. 후속 작업에서 필요한 요청 조건을 유지하되 보고서에 자격 증명 원문을 반복하지 않습니다.

공식 사용법: [ffuf](https://github.com/ffuf/ffuf), [sqlmap](https://github.com/sqlmapproject/sqlmap/wiki/Usage), [Nmap](https://nmap.org/book/man.html).
