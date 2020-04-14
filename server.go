// Copyright 2019 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gerritlinter

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Formatter is a definition of a formatting engine
type Formatter interface {
	// Format returns the files but formatted. All files are
	// assumed to have the same language.
	Format(in []File, outSink io.Writer) (out []FormattedFile, err error)
}

// FormatterConfig defines the mapping configurable
type FormatterConfig struct {
	// Regex is the typical filename regexp to use
	Regex *regexp.Regexp

	// Query is used to filter inside Gerrit
	Query string

	// The formatter
	Formatter Formatter
}

// formatters holds all the formatters supported
var formatters = map[string]*FormatterConfig{
	"commitmsg": {
		Regex:     regexp.MustCompile(`^/COMMIT_MSG$`),
		Formatter: &commitMsgFormatter{},
	},
}

func init() {
	// Add path to self to $PATH, for easy deployment.
	if exe, err := os.Executable(); err == nil {
		os.Setenv("PATH", filepath.Dir(exe)+":"+os.Getenv("PATH"))
	}

	gjf, err := exec.LookPath("google-java-format.jar")
	if err == nil {
		formatters["java"] = &FormatterConfig{
			Regex: regexp.MustCompile(`\.java$`),
			Query: "ext:java",
			Formatter: &toolFormatter{
				bin:  "java",
				args: []string{"-jar", gjf, "-i"},
			},
		}
	} else {
		log.Printf("LookPath google-java-format: %v PATH=%s", err, os.Getenv("PATH"))
	}

	bzl, err := exec.LookPath("buildifier")
	if err == nil {
		formatters["bzl"] = &FormatterConfig{
			Regex: regexp.MustCompile(`(\.bzl|/BUILD|^BUILD)$`),
			Query: "(ext:bzl OR file:BUILD OR file:WORKSPACE)",
			Formatter: &toolFormatter{
				bin:  bzl,
				args: []string{"-mode=fix"},
			},
		}
	} else {
		log.Printf("LookPath buildifier: %v, PATH=%s", err, os.Getenv("PATH"))
	}

	gofmt, err := exec.LookPath("gofmt")
	if err == nil {
		formatters["go"] = &FormatterConfig{
			Regex: regexp.MustCompile(`\.go$`),
			Query: "ext:go",
			Formatter: &toolFormatter{
				bin:  gofmt,
				args: []string{"-w"},
			},
		}
	} else {
		log.Printf("LookPath gofmt: %v, PATH=%s", err, os.Getenv("PATH"))
	}
}

func GetFormatter(lang string) (*FormatterConfig, bool) {
	footerPrefix := "commitfooter-"
	if strings.HasPrefix(lang, footerPrefix) {
		return &FormatterConfig{
			Regex: regexp.MustCompile(`^/COMMIT_MSG$`),
			Formatter: &commitFooterFormatter{
				Footer: lang[len(footerPrefix):],
			},
		}, true
	}

	cfg, ok := formatters[lang]
	return cfg, ok
}

// IsSupported returns if the given language is supported.
func IsSupported(lang string) bool {
	_, ok := GetFormatter(lang)
	return ok
}

// SupportedLanguages returns a list of languages.
func SupportedLanguages() []string {
	var r []string
	for l := range formatters {
		r = append(r, l)
	}
	sort.Strings(r)
	return r
}

func splitByLang(in []File) map[string][]File {
	res := map[string][]File{}
	for _, f := range in {
		res[f.Language] = append(res[f.Language], f)
	}
	return res
}

// Format formats all the files in the request for which a formatter exists.
func Format(req *FormatRequest, rep *FormatReply) error {
	for _, f := range req.Files {
		if f.Language == "" {
			return fmt.Errorf("file %q has empty language", f.Name)
		}
	}

	for language, fs := range splitByLang(req.Files) {
		var buf bytes.Buffer
		entry, ok := GetFormatter(language)
		if !ok {
			return fmt.Errorf("linter: no formatter for %q", language)
		}
		out, err := entry.Formatter.Format(fs, &buf)
		if err != nil {
			return err
		}

		if len(out) > 0 && out[0].Message == "" {
			out[0].Message = buf.String()
		}
		rep.Files = append(rep.Files, out...)
	}
	return nil
}

type commitMsgFormatter struct{}

func (f *commitMsgFormatter) Format(in []File, outSink io.Writer) (out []FormattedFile, err error) {
	complaint := checkCommitMessage(string(in[0].Content))
	ff := FormattedFile{}
	ff.Name = in[0].Name
	if complaint != "" {
		ff.Message = complaint
	} else {
		ff.Content = in[0].Content
	}
	out = append(out, ff)
	return out, nil
}

func checkCommitMessage(msg string) (complaint string) {
	lines := strings.Split(msg, "\n")
	if len(lines) < 2 {
		return "must have multiple lines"
	}

	if len(lines[1]) > 1 {
		return "subject and body must be separated by blank line"
	}

	if len(lines[0]) > 70 {
		return "subject must be less than 70 chars"
	}

	if strings.HasSuffix(lines[0], ".") {
		return "subject must not end in '.'"
	}

	return ""
}

type commitFooterFormatter struct {
	Footer string
}

func (f *commitFooterFormatter) Format(in []File, outSink io.Writer) (out []FormattedFile, err error) {
	complaint := checkCommitFooter(string(in[0].Content), f.Footer)
	ff := FormattedFile{}
	ff.Name = in[0].Name
	if complaint != "" {
		ff.Message = complaint
	} else {
		ff.Content = in[0].Content
	}
	out = append(out, ff)
	return out, nil
}

func checkCommitFooter(message, footer string) string {
	if len(footer) == 0 {
		return "required footer should be non-empty"
	}

	blocks := strings.Split(message, "\n\n")
	if len(blocks) < 2 {
		return "gerrit changes must have two paragraphs."
	}

	footerBlock := blocks[len(blocks)-1]
	lines := strings.Split(footerBlock, "\n")
	for _, l := range lines {
		fields := strings.SplitN(l, ":", 2)
		if len(fields) < 2 {
			continue
		}

		if fields[0] != footer {
			continue
		}

		value := fields[1]
		if !strings.HasPrefix(value, " ") {
			return fmt.Sprintf("footer %q should have space after ':'", fields[1])
		}

		// length limit?
		return ""
	}

	return fmt.Sprintf("footer %q not found", footer)
}

type toolFormatter struct {
	bin  string
	args []string
}

func (f *toolFormatter) Format(in []File, outSink io.Writer) (out []FormattedFile, err error) {
	cmd := exec.Command(f.bin, f.args...)

	tmpDir, err := ioutil.TempDir("", "gerritfmt")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	for _, f := range in {
		dir, base := filepath.Split(f.Name)
		dir = filepath.Join(tmpDir, dir)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}

		if err := ioutil.WriteFile(filepath.Join(dir, base), f.Content, 0644); err != nil {
			return nil, err
		}

		cmd.Args = append(cmd.Args, f.Name)
	}
	cmd.Dir = tmpDir

	var errBuf, outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	log.Println("running", cmd.Args, "in", tmpDir)
	if err := cmd.Run(); err != nil {
		log.Printf("error %v, stderr %s, stdout %s", err, errBuf.String(),
			outBuf.String())
		return nil, err
	}

	for _, f := range in {
		c, err := ioutil.ReadFile(filepath.Join(tmpDir, f.Name))
		if err != nil {
			return nil, err
		}

		out = append(out, FormattedFile{
			File: File{
				Name:    f.Name,
				Content: c,
			},
		})
	}

	return out, nil
}
