package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/emersion/go-message"
	messageMail "github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// 미리 컴파일한 정규식: HTML 파싱 실패 시 fallback 용도로 사용
var urlRegex = regexp.MustCompile(`https?://[^\s"']+`)

func init() {
	// go-message의 CharsetReader를 설정하여 다양한 문자셋 지원
	message.CharsetReader = func(cs string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(cs) {
		case "euc-kr", "ks_c_5601-1987":
			return transform.NewReader(input, korean.EUCKR.NewDecoder()), nil
		case "iso-8859-1":
			return transform.NewReader(input, charmap.ISO8859_1.NewDecoder()), nil
		case "iso-8859-2":
			return transform.NewReader(input, charmap.ISO8859_2.NewDecoder()), nil
		case "windows-1252":
			return transform.NewReader(input, charmap.Windows1252.NewDecoder()), nil
		case "windows-1251":
			return transform.NewReader(input, charmap.Windows1251.NewDecoder()), nil
		case "iso-2022-jp":
			return transform.NewReader(input, japanese.ISO2022JP.NewDecoder()), nil
		case "ascii":
			return input, nil
		case "gb2312":
			return transform.NewReader(input, simplifiedchinese.GB18030.NewDecoder()), nil
		case "big5":
			return transform.NewReader(input, traditionalchinese.Big5.NewDecoder()), nil
		default:
			return charset.NewReaderLabel(cs, input)
		}
	}
}

// EmailRecord는 EML 파일에서 추출한 정보를 담는 구조체입니다.
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
	var workerCount int

	flag.BoolVar(&jsonOutput, "json", false, "JSON 형식으로 출력")
	flag.BoolVar(&csvOutput, "csv", false, "CSV 형식으로 출력")
	flag.BoolVar(&recursive, "r", false, "재귀적으로 디렉토리 탐색")
	flag.StringVar(&htmlOutDir, "eml2html-to", "", "지정한 경로에 EML 파일을 HTML로 변환하여 저장")
	flag.BoolVar(&renameByHeader, "rename-by-header", false, "EML 파일의 날짜-제목 기반으로 파일명을 변경")
	flag.StringVar(&renameByHeaderTo, "rename-by-header-to", "", "지정한 경로에 EML 파일을 복사한 후 날짜-제목 기반으로 파일명을 변경")
	// 동시 처리할 워커 수 (기본값: CPU 코어 수)
	flag.IntVar(&workerCount, "workers", runtime.NumCPU(), "동시 처리 워커 수")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "사용법:\n")
		fmt.Fprintf(os.Stderr, "  %s [-r] [-json|-csv] [-eml2html-to PATH] [-rename-by-header] [-rename-by-header-to PATH] [디렉토리 경로]\n", os.Args[0])
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
	var filePaths []string
	var err error
	if recursive {
		filePaths, err = collectFilePathsRecursive(inputRoot)
	} else {
		filePaths, err = collectFilePathsNonRecursive(inputRoot)
	}
	if err != nil {
		log.Fatalf("[ERROR] 파일 경로 수집 실패: %v", err)
	}

	// 동시 처리로 EML 파일을 파싱하고 필요 시 HTML 변환 및 재명명 수행
	records := processFilesConcurrently(filePaths, workerCount, htmlOutDir, renameByHeader, renameByHeaderTo, inputRoot)

	// 출력 옵션에 따라 결과를 화면에 출력
	if htmlOutDir != "" || renameByHeader || renameByHeaderTo != "" {
		fmt.Fprintf(os.Stderr, "[DEBUG] 파일 변환 및 재명명 작업 완료. 화면 출력 생략.\n")
	} else {
		printOutput(records, jsonOutput, csvOutput)
	}
}

// collectFilePathsRecursive는 주어진 디렉토리를 재귀적으로 탐색하여 .eml 파일 경로 목록을 반환합니다.
func collectFilePathsRecursive(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && shouldProcessFile(d.Name()) {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// collectFilePathsNonRecursive는 주어진 디렉토리에서 .eml 파일 경로만 반환합니다.
func collectFilePathsNonRecursive(root string) ([]string, error) {
	var paths []string
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && shouldProcessFile(entry.Name()) {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	return paths, nil
}

func shouldProcessFile(name string) bool {
	matched, _ := filepath.Match("*.eml", name)
	return matched
}

type task struct {
	path string
}

type result struct {
	record EmailRecord
	err    error
}

// processFilesConcurrently는 파일 경로 목록을 받아 지정한 워커 수로 병렬 처리합니다.
func processFilesConcurrently(paths []string, workerCount int, htmlOutDir string, renameByHeader bool, renameByHeaderTo string, inputRoot string) []EmailRecord {
	tasks := make(chan task, len(paths))
	results := make(chan result, len(paths))
	var wg sync.WaitGroup

	worker := func() {
		for t := range tasks {
			rec, htmlContent, err := processEmlFile(t.path)
			if err != nil {
				results <- result{err: err}
				continue
			}
			// HTML 파일 저장
			if htmlOutDir != "" {
				if err := writeHtmlFile(t.path, inputRoot, htmlOutDir, htmlContent); err != nil {
					log.Printf("[WARN] HTML 파일 생성 실패: %s (%v)", t.path, err)
				}
			}
			// 파일 재명명 또는 복사
			if renameByHeaderTo != "" {
				if err := renameFileTo(t.path, inputRoot, renameByHeaderTo, rec); err != nil {
					log.Printf("[WARN] 파일 복사 재명명 실패: %s (%v)", t.path, err)
				}
			} else if renameByHeader {
				if err := renameFile(t.path, rec); err != nil {
					log.Printf("[WARN] 파일 재명명 실패: %s (%v)", t.path, err)
				}
			}
			results <- result{record: rec}
		}
		wg.Done()
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}
	for _, path := range paths {
		tasks <- task{path: path}
	}
	close(tasks)
	wg.Wait()
	close(results)

	var records []EmailRecord
	for res := range results {
		if res.err != nil {
			log.Printf("[WARN] 파일 처리 실패: %v", res.err)
			continue
		}
		records = append(records, res.record)
	}
	return records
}

// processEmlFile는 버퍼링을 적용하여 EML 파일을 파싱합니다.
func processEmlFile(filePath string) (EmailRecord, string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return EmailRecord{}, "", err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	mr, err := messageMail.CreateReader(br)
	if err != nil {
		return EmailRecord{}, "", err
	}

	h := mr.Header

	subject, err := h.Subject()
	if err != nil {
		subject = ""
	}

	fromList, err := h.AddressList("From")
	var fromName, fromEmail string
	if err == nil && len(fromList) > 0 {
		fromName = fromList[0].Name
		fromEmail = fromList[0].Address
	}

	toList, err := h.AddressList("To")
	var toName, toEmail string
	if err == nil && len(toList) > 0 {
		toName = toList[0].Name
		toEmail = toList[0].Address
	}

	date, err := h.Date()
	var sentDate string
	if err == nil {
		sentDate = date.Format("2006-01-02 15:04:05")
	}

	originIP := h.Get("X-Originating-IP")

	var htmlContent string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return EmailRecord{}, "", err
		}
		ct := p.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "text/html") {
			body, err := io.ReadAll(p.Body)
			if err != nil {
				continue
			}
			htmlContent = string(body)
			break
		}
	}

	urls := extractUrls(htmlContent)
	urlList := strings.Join(urls, "\n")
	var urlDomains []string
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

	return record, htmlContent, nil
}

func renameFile(filePath string, record EmailRecord) error {
	dir := filepath.Dir(filePath)
	newTime := formatTime(record.SentDate)
	newName := sanitizeFilename(fmt.Sprintf("%s %s.eml", newTime, record.Subject))
	newPath := filepath.Join(dir, newName)
	return os.Rename(filePath, newPath)
}

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

// formatTime는 "2006-01-02 15:04:05" 형식을 "2006-01-02_150405" 형태로 변환합니다.
func formatTime(sentDate string) string {
	if sentDate == "" {
		return "unknown"
	}
	parts := strings.Split(sentDate, " ")
	if len(parts) >= 2 {
		datePart := parts[0]
		timePart := strings.ReplaceAll(parts[1], ":", "")
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

func extractUrls(htmlContent string) []string {
	var urls []string
	if strings.TrimSpace(htmlContent) == "" {
		return urls
	}
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return urlRegex.FindAllString(htmlContent, -1)
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
