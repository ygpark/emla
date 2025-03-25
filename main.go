// 파일명: main.go
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
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

	flag.BoolVar(&jsonOutput, "json", false, "JSON 형식으로 출력")
	flag.BoolVar(&csvOutput, "csv", false, "CSV 형식으로 출력")
	flag.BoolVar(&recursive, "r", false, "재귀적으로 디렉토리 탐색")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "사용법:\n")
		fmt.Fprintf(os.Stderr, "  %s [-json|-csv] [디렉토리 경로]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -r [-json|-csv] [디렉토리 경로]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	if !jsonOutput && !csvOutput {
		csvOutput = true // 기본은 CSV 출력
	}

	root := flag.Arg(0)

	var records []EmailRecord
	var err error

	if recursive {
		records, err = walkDirectory(root)
	} else {
		records, err = scanDirectoryOnce(root)
	}

	if err != nil {
		log.Fatalf("[ERROR] 디렉토리 처리 중 오류 발생: %v", err)
	}

	printOutput(records, jsonOutput, csvOutput)
}

func shouldProcessFile(name string) bool {
	matched, _ := filepath.Match("*.eml", name)
	return matched
}

func walkDirectory(root string) ([]EmailRecord, error) {
	var records []EmailRecord

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if shouldProcessFile(d.Name()) {
			record, err := processEmlFile(path)
			if err != nil {
				log.Printf("[WARN] 파일 처리 실패: %s (%v)", path, err)
				return nil
			}
			records = append(records, record)
		}
		return nil
	})
	return records, err
}

func scanDirectoryOnce(root string) ([]EmailRecord, error) {
	var records []EmailRecord

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if shouldProcessFile(entry.Name()) {
			path := filepath.Join(root, entry.Name())
			record, err := processEmlFile(path)
			if err != nil {
				log.Printf("[WARN] 파일 처리 실패: %s (%v)", path, err)
				continue
			}
			records = append(records, record)
		}
	}
	return records, nil
}

func processEmlFile(filePath string) (EmailRecord, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return EmailRecord{}, err
	}
	defer file.Close()

	env, err := enmime.ReadEnvelope(file)
	if err != nil {
		return EmailRecord{}, err
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

	return EmailRecord{
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
	}, nil
}

func parseAddress(input string) (string, string) {
	addr, err := mail.ParseAddress(input)
	if err == nil {
		return addr.Name, addr.Address
	}
	// 개선된 정규표현식: 이름과 이메일을 분리하여 추출
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
		// HTML 파싱 실패 시 fallback: 정규표현식을 사용하여 URL 추출
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

	// URL 중복 제거
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
