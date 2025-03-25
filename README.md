# emla

**emla**는 `.eml` 이메일 파일을 빠르게 분석하고, 필요한 정보를 다양한 형식으로 추출 및 변환할 수 있는 Go 기반의 명령줄 도구입니다. 이메일 수집, 디지털 포렌식, 보안 분석 등 다양한 목적에 활용할 수 있습니다.

## 주요 기능

✅ **CSV/JSON 요약 출력**  
✅ **HTML 콘텐츠 추출**  
✅ **이메일 헤더 기반 파일명 재정렬 및 복사**  
✅ **디렉토리 재귀 탐색 처리**

---

## 설치 방법

Go가 설치되어 있다면 아래 명령 한 줄로 설치할 수 있습니다:

```bash
go install github.com/ygaprk/emla@latest
```

또는 수동 설치:

```bash
git clone https://github.com/ygaprk/emla.git
cd emla
go build -o emla main.go
```

---

## 빠른 사용법

```bash
emla [옵션] <EML_파일_또는_디렉토리>
```

### 예시

- HTML 콘텐츠 추출:

  ```bash
  emla -eml2html-dir ./html_output ./emails
  ```

- 이메일 파일명을 헤더 기반으로 재정렬:

  ```bash
  emla -rename-by-header ./emails
  ```

- 헤더 기반 재명명 + 복사:

  ```bash
  emla -rename-by-header-to ./renamed ./emails
  ```

- 재귀적으로 디렉토리 내 모든 EML 파일을 탐색하여 CSV 출력:

  ```bash
  emla -r ./emails
  ```

---

## 옵션 요약

| 옵션                        | 설명                                                 |
| --------------------------- | ---------------------------------------------------- |
| `-json`                     | JSON 형식으로 결과 출력                              |
| `-csv`                      | CSV 형식으로 결과 출력 (기본값)                      |
| `-r`                        | 디렉토리를 재귀적으로 탐색                           |
| `-eml2html-dir PATH`        | HTML 콘텐츠를 추출하여 지정된 경로에 저장            |
| `-rename-by-header`         | 헤더 기반으로 원본 파일명을 직접 재명명              |
| `-rename-by-header-to PATH` | 헤더 기반으로 파일명을 재명명하여 지정된 경로에 복사 |

📌 `-eml2html-dir`, `-rename-by-header`, `-rename-by-header-to` 중 하나라도 사용하면 CSV/JSON 출력은 생략됩니다.

📁 파일명 형식 예시: `2024-03-26_153015 제목.eml`

---

## 이메일 정보 추출 예시

- **보낸 사람 / 받는 사람** 이름 및 이메일
- **날짜** (YYYY-MM-DD HH:MM:SS)
- **제목**
- **X-Originating-IP**
- **본문 내 URL / 도메인 목록**

---

## 라이선스

이 프로젝트는 [MIT License](LICENSE)에 따라 배포됩니다.

---

## 프로젝트 주소

GitHub: [https://github.com/ygaprk/emla](https://github.com/ygaprk/emla)
