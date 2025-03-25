// 파일명: main.go
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jhillyerd/enmime"
	"golang.org/x/net/html"
)

type EmailRecord struct {
	URLDomains   string
	Folder       string
	Subject      string
	FromName     string
	FromEmail    string
	ToName       string
	ToEmail      string
	SentDate     string
	IP           string
	URLs         string
	OriginalFile string
}

func main() {

	var jsonOutput bool
	var csvOutput bool
	var recursive bool
	var htmlOutDir string
	var renameByHeader bool
	var renameByHeaderTo string

	flag.BoolVar(&jsonOutput, "json", false, "JSON 형식으로 출력")
	flag.BoolVar(&csvOutput, "csv", false, "CSV 형식으로 출력")
	flag.BoolVar(&recursive, "r", false, "재귀적으로 디렉토리 탐색")
	flag.StringVar(&htmlOutDir, "eml2html-to", "", "지정한 경로에 EML 파일을 HTML로 변환하여 저장")
	flag.BoolVar(&renameByHeader, "rename-by-header", false, "EML 파일의 날짜-제목 기반으로 파일명을 변경")
	flag.StringVar(&renameByHeaderTo, "rename-by-header-to", "", "지정한 경로에 EML 파일을 복사한 후 날짜-제목 기반으로 파일명을 변경")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "사용법:\n")
		fmt.Fprintf(os.Stderr, "  %s [-r] [-json|-csv] [-eml2html PATH] [-rename-by-header] [-rename-by-header-to PATH] [디렉토리 경로]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	// 기본 출력은 CSV
	if !jsonOutput && !csvOutput {
		csvOutput = true
	}

	inputRoot := flag.Arg(0)

	var records []EmailRecord
	var err error

	if recursive {
		records, err = walkDirectory(inputRoot, htmlOutDir, renameByHeader, renameByHeaderTo)
	} else {
		records, err = scanDirectoryOnce(inputRoot, htmlOutDir, renameByHeader, renameByHeaderTo)
	}

	if err != nil {
		log.Fatalf("[ERROR] 디렉토리 처리 중 오류 발생: %v", err)
	}

	// 옵션 중 하나라도 지정되면 화면에 출력하지 않습니다.
	if htmlOutDir != "" || renameByHeader || renameByHeaderTo != "" {
		fmt.Fprintf(os.Stderr, "[DEBUG] 파일 변환 및 재명명 작업 완료. 화면 출력 생략.\n")
	} else {
		printOutput(records, jsonOutput, csvOutput)
	}
}

func shouldProcessFile(name string) bool {
	matched, _ := filepath.Match("*.eml", name)
	return matched
}

func walkDirectory(inputRoot, htmlOutDir string, renameByHeader bool, renameByHeaderTo string) ([]EmailRecord, error) {
	var records []EmailRecord

	err := filepath.WalkDir(inputRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if shouldProcessFile(d.Name()) {
			record, htmlContent, err := processEmlFile(path)
			if err != nil {
				log.Printf("[WARN] 파일 처리 실패: %s (%v)", path, err)
				return nil
			}
			if htmlOutDir != "" {
				if err := writeHtmlFile(path, inputRoot, htmlOutDir, htmlContent); err != nil {
					log.Printf("[WARN] HTML 파일 생성 실패: %s (%v)", path, err)
				}
			}
			// 우선순위: rename-by-header-to 옵션이 있으면 그 기능 사용, 없으면 in-place rename
			if renameByHeaderTo != "" {
				if err := renameFileTo(path, inputRoot, renameByHeaderTo, record); err != nil {
					log.Printf("[WARN] 파일 복사 재명명 실패: %s (%v)", path, err)
				}
			} else if renameByHeader {
				if err := renameFile(path, record); err != nil {
					log.Printf("[WARN] 파일 재명명 실패: %s (%v)", path, err)
				}
			}
			records = append(records, record)
		}
		return nil
	})
	return records, err
}

func scanDirectoryOnce(inputRoot, htmlOutDir string, renameByHeader bool, renameByHeaderTo string) ([]EmailRecord, error) {
	var records []EmailRecord

	entries, err := os.ReadDir(inputRoot)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if shouldProcessFile(entry.Name()) {
			path := filepath.Join(inputRoot, entry.Name())
			record, htmlContent, err := processEmlFile(path)
			if err != nil {
				log.Printf("[WARN] 파일 처리 실패: %s (%v)", path, err)
				continue
			}
			if htmlOutDir != "" {
				if err := writeHtmlFile(path, inputRoot, htmlOutDir, htmlContent); err != nil {
					log.Printf("[WARN] HTML 파일 생성 실패: %s (%v)", path, err)
				}
			}
			if renameByHeaderTo != "" {
				if err := renameFileTo(path, inputRoot, renameByHeaderTo, record); err != nil {
					log.Printf("[WARN] 파일 복사 재명명 실패: %s (%v)", path, err)
				}
			} else if renameByHeader {
				if err := renameFile(path, record); err != nil {
					log.Printf("[WARN] 파일 재명명 실패: %s (%v)", path, err)
				}
			}
			records = append(records, record)
		}
	}
	return records, nil
}

func processEmlFile(filePath string) (EmailRecord, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return EmailRecord{}, "", err
	}
	defer file.Close()

	env, err := enmime.ReadEnvelope(file)
	if err != nil {
		return EmailRecord{}, "", err
	}

	header := env.Root.Header
	fromName, fromEmail := parseAddress(header.Get("From"))
	toName, toEmail := parseAddress(header.Get("To"))

	subject := env.GetHeader("Subject")
	originIP := strings.Trim(header.Get("X-Originating-IP"), "[]")

	var sentDate string
	if dateStr := header.Get("Date"); dateStr != "" {
		if t, err := mail.ParseDate(dateStr); err == nil {
			sentDate = t.Format("2006-01-02 15:04:05")
		}
	}

	urls := extractUrls(env.HTML)
	urlList := strings.Join(urls, "\n")
	urlDomains := make([]string, 0)
	for _, u := range urls {
		if parsed, err := url.Parse(u); err == nil {
			urlDomains = append(urlDomains, parsed.Host)
		}
	}

	folder := filepath.Base(filepath.Dir(filePath))
	originalFile := filepath.Base(filePath)

	record := EmailRecord{
		Folder:       folder,
		Subject:      subject,
		FromName:     fromName,
		FromEmail:    fromEmail,
		ToName:       toName,
		ToEmail:      toEmail,
		SentDate:     sentDate,
		IP:           strings.ReplaceAll(originIP, ",", "\n"),
		URLs:         urlList,
		URLDomains:   strings.Join(urlDomains, "\n"),
		OriginalFile: originalFile,
	}
	return record, env.HTML, nil
}

// renameFile는 원본 파일의 이름을 in-place로 변경합니다.
func renameFile(filePath string, record EmailRecord) error {
	dir := filepath.Dir(filePath)
	newTime := formatTime(record.SentDate)
	newName := sanitizeFilename(fmt.Sprintf("%s %s.eml", newTime, record.Subject))
	newPath := filepath.Join(dir, newName)
	return os.Rename(filePath, newPath)
}

// renameFileTo는 원본 파일은 그대로 두고, 새 디렉토리에 복사하여 재명명합니다.
func renameFileTo(filePath, inputRoot, outputDir string, record EmailRecord) error {
	relPath, err := filepath.Rel(inputRoot, filePath)
	if err != nil {
		return err
	}
	newTime := formatTime(record.SentDate)
	newName := sanitizeFilename(fmt.Sprintf("%s %s.eml", newTime, record.Subject))
	newRelPath := filepath.Join(filepath.Dir(relPath), newName)
	newPath := filepath.Join(outputDir, newRelPath)

	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return err
	}

	srcFile, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(newPath)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// formatTime는 "2006-01-02 15:04:05" 형식을 받아 "2006-01-02_150405" 형태로 변환합니다.
// 시간 부분에서는 하이픈 없이 HHMMSS 형식으로 만듭니다.
func formatTime(sentDate string) string {
	if sentDate == "" {
		return "unknown"
	}
	parts := strings.Split(sentDate, " ")
	if len(parts) >= 2 {
		datePart := parts[0]
		timePart := strings.ReplaceAll(parts[1], ":", "") // "15:04:05" -> "150405"
		return datePart + "_" + timePart
	}
	return sentDate
}

func sanitizeFilename(name string) string {
	invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	for _, char := range invalidChars {
		name = strings.ReplaceAll(name, char, "_")
	}
	return name
}

func writeHtmlFile(filePath, inputRoot, htmlOutDir, htmlContent string) error {
	relPath, err := filepath.Rel(inputRoot, filePath)
	if err != nil {
		return err
	}
	newRelPath := strings.TrimSuffix(relPath, filepath.Ext(relPath)) + ".html"
	outPath := filepath.Join(htmlOutDir, newRelPath)

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(outPath, []byte(htmlContent), 0644)
}

func parseAddress(input string) (string, string) {
	addr, err := mail.ParseAddress(input)
	if err == nil {
		return addr.Name, addr.Address
	}
	re := regexp.MustCompile(`(?i)^(?:"?([^"]*)"?\s*)?<?([a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,})>?$`)
	if match := re.FindStringSubmatch(strings.TrimSpace(input)); len(match) == 3 {
		name := strings.TrimSpace(match[1])
		email := strings.TrimSpace(match[2])
		return name, email
	}
	return "", strings.Trim(input, " \"")
}

func extractUrls(htmlContent string) []string {
	var urls []string

	if strings.TrimSpace(htmlContent) == "" {
		return urls
	}

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		re := regexp.MustCompile(`https?://[^\s"']+`)
		matches := re.FindAllString(htmlContent, -1)
		return matches
	}

	var crawler func(*html.Node)
	crawler = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key == "href" && strings.HasPrefix(attr.Val, "http") {
					urls = append(urls, attr.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			crawler(c)
		}
	}
	crawler(doc)

	unique := make(map[string]struct{})
	var result []string
	for _, u := range urls {
		if _, exists := unique[u]; !exists {
			unique[u] = struct{}{}
			result = append(result, u)
		}
	}
	return result
}

func writeCsvToStdout(records []EmailRecord) {
	writer := csv.NewWriter(os.Stdout)
	defer writer.Flush()

	headers := []string{
		"폴더", "제목", "보낸사람 이름", "보낸사람 이메일",
		"받은사람 이름", "받은사람 이메일", "보낸 날짜", "X-Originating-IP",
		"본문URL", "본문URL(도메인)", "원본",
	}
	writer.Write(headers)

	for _, r := range records {
		row := []string{
			r.Folder,
			r.Subject,
			r.FromName,
			r.FromEmail,
			r.ToName,
			r.ToEmail,
			r.SentDate,
			r.IP,
			r.URLs,
			r.URLDomains,
			r.OriginalFile,
		}
		writer.Write(row)
	}
}

func printJSON(records []EmailRecord) {
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Fatalf("[ERROR] JSON 변환 실패: %v", err)
	}
	fmt.Println(string(b))
}

func printOutput(records []EmailRecord, jsonOutput bool, csvOutput bool) {
	if jsonOutput {
		printJSON(records)
	} else if csvOutput {
		writeCsvToStdout(records)
	}
}
