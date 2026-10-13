// Command fetch downloads the Loghub 2.0 datasets, checks their checksums and writes the files the bench reads
//
//	go run ./bench/fetch -dir <datasets dir> [-datasets Apache,HDFS]
//
// Each dataset becomes a directory of three files
// 1. content.txt with one log message per line
// 2. truth.txt with the ground truth event id of each line
// 3. templates.tsv with the template of each event
package main

import (
	"archive/zip"
	"bufio"
	"crypto/md5"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The Loghub 2.0 archives on Zenodo record 8275861 with the MD5 sums Zenodo publishes
var archives = map[string]string{
	"Apache":      "0c52d166e5ee118b2d19d8a57cd2d0b4",
	"BGL":         "521ee643375bb6e9d0dd88636ff7ece1",
	"HDFS":        "f23880dd4938379a535ab71a8d27a798",
	"HPC":         "07d035b50ede61f7850e0453e43f85bb",
	"Hadoop":      "e8d413f0ea3f3e90956f185da9d635a8",
	"HealthApp":   "6b90126e1c78591454aef10ecc1da098",
	"Linux":       "a1a1db599519eb6b12b3368f37d274f7",
	"Mac":         "e5b392e013be0f10a55b5b009f4b9c9d",
	"OpenSSH":     "fef920579c366462ea7d341af8f533d8",
	"OpenStack":   "707faeedf8b1b9f920c073114139a023",
	"Proxifier":   "2e4b67dc68c3a4f1c18e267c341c52a2",
	"Spark":       "ca0ef9c6e8ece0df602793bd302e03a6",
	"Thunderbird": "a8d365d003845c3bef61661375513910",
	"Zookeeper":   "e696a94a9b703eb3fd93a2c78e3050ed",
}

const zenodo = "https://zenodo.org/records/8275861/files/%s.zip?download=1"

// The largest archive takes minutes on a slow link
// Only a stalled download hits this
var client = &http.Client{Timeout: time.Hour}

func main() {
	dir := flag.String("dir", "", "directory the datasets go to")
	only := flag.String("datasets", "all", "comma separated dataset names, or all")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "fetch: -dir is required")
		os.Exit(2)
	}
	names := make([]string, 0, len(archives))
	for name := range archives {
		names = append(names, name)
	}
	sort.Strings(names)
	if *only != "all" {
		names = strings.Split(*only, ",")
	}
	for _, name := range names {
		if err := fetch(*dir, name); err != nil {
			fmt.Fprintf(os.Stderr, "fetch %s: %v\n", name, err)
			os.Exit(1)
		}
	}
}

func fetch(dir, name string) error {
	sum, ok := archives[name]
	if !ok {
		return errors.New("not a Loghub 2.0 dataset")
	}
	out := filepath.Join(dir, name)
	if _, err := os.Stat(filepath.Join(out, "templates.tsv")); err == nil {
		fmt.Printf("%s: ready\n", name)
		return nil
	}
	zipPath := filepath.Join(dir, "zip", name+".zip")
	if err := download(zipPath, fmt.Sprintf(zenodo, name), sum); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	n, err := convert(zipPath, out)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %d lines\n", name, n)
	return nil
}

// Downloads url to path unless a file with the right MD5 is already there
func download(path, url, sum string) error {
	if got, err := md5File(path); err == nil && got == sum {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	res, err := client.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, res.Status)
	}
	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	got, err := md5File(path + ".part")
	if err != nil {
		return err
	}
	if got != sum {
		return fmt.Errorf("checksum %s, want %s", got, sum)
	}
	return os.Rename(path+".part", path)
}

func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Splits the structured CSV of the archive into content and truth and templates
func convert(zipPath, out string) (n int, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = zr.Close() }()
	var structured *zip.File
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "_full.log_structured.csv") {
			structured = f
		}
	}
	if structured == nil {
		return 0, errors.New("no structured csv in the archive")
	}
	rc, err := structured.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = rc.Close() }()
	content, err := os.Create(filepath.Join(out, "content.txt"))
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, content.Close()) }()
	truth, err := os.Create(filepath.Join(out, "truth.txt"))
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, truth.Close()) }()
	cw, tw := bufio.NewWriterSize(content, 1<<20), bufio.NewWriterSize(truth, 1<<20)
	r := csv.NewReader(bufio.NewReaderSize(rc, 1<<20))
	r.ReuseRecord = true
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return 0, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	for _, name := range []string{"Content", "EventId", "EventTemplate"} {
		if _, ok := col[name]; !ok {
			return 0, fmt.Errorf("structured csv has no %s column", name)
		}
	}
	ci, ei, ti := col["Content"], col["EventId"], col["EventTemplate"]
	// a message never spans lines in the content file and a template never splits its row
	oneLine := strings.NewReplacer("\r", " ", "\n", " ")
	oneField := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")
	templates := map[string]string{}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return n, err
		}
		fmt.Fprintln(cw, oneLine.Replace(rec[ci]))
		fmt.Fprintln(tw, rec[ei])
		if _, ok := templates[rec[ei]]; !ok {
			templates[rec[ei]] = oneField.Replace(rec[ti])
		}
		n++
	}
	if err := cw.Flush(); err != nil {
		return n, err
	}
	if err := tw.Flush(); err != nil {
		return n, err
	}
	ids := make([]string, 0, len(templates))
	for id := range templates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s\t%s\n", id, templates[id])
	}
	return n, os.WriteFile(filepath.Join(out, "templates.tsv"), []byte(b.String()), 0o644)
}
