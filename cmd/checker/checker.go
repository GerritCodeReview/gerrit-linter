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

package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/rpc"
	"strconv"
	"strings"
	"time"

	linter "github.com/google/gerrit-linter"
	"github.com/google/gerrit-linter/gerrit"
)

// gerritChecker run formatting checks against a gerrit server.
type gerritChecker struct {
	server *gerrit.Server
	delay  time.Duration
	todo   chan *gerrit.PendingChecksInfo
}

// checkerScheme is the scheme by which we are registered in the Gerrit server.
const checkerScheme = "fmt"

// ListCheckers returns all the checkers for our scheme.
func (gc *gerritChecker) ListCheckers() ([]*gerrit.CheckerInfo, error) {
	c, err := gc.server.GetPath("a/plugins/checks/checkers/")
	if err != nil {
		log.Fatalf("ListCheckers: %v", err)
	}

	var out []*gerrit.CheckerInfo
	if err := gerrit.Unmarshal(c, &out); err != nil {
		return nil, err
	}

	filtered := out[:0]
	for _, o := range out {
		if !strings.HasPrefix(o.UUID, checkerScheme+":") {
			continue
		}
		if _, ok := checkerLanguage(o.UUID); !ok {
			continue
		}

		filtered = append(filtered, o)
	}
	return filtered, nil
}

// PostChecker creates or changes a checker. It sets up a checker on
// the given repo, for the given language.
func (gc *gerritChecker) PostChecker(repo, language string, update bool) (*gerrit.CheckerInfo, error) {
	hash := sha1.New()
	hash.Write([]byte(repo))

	uuid := fmt.Sprintf("%s:%s-%x", checkerScheme, language, hash.Sum(nil))
	in := gerrit.CheckerInput{
		UUID:        uuid,
		Name:        language + " formatting",
		Repository:  repo,
		Description: "check source code formatting.",
		Status:      "ENABLED",
		Query:       linter.Formatters[language].Query,
	}

	body, err := json.Marshal(&in)
	if err != nil {
		return nil, err
	}

	path := "a/plugins/checks/checkers/"
	if update {
		path += uuid
	}
	content, err := gc.server.PostPath(path, "application/json", body)
	if err != nil {
		return nil, err
	}

	out := gerrit.CheckerInfo{}
	if err := gerrit.Unmarshal(content, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// checkerLanguage extracts the language to check for from a checker UUID.
func checkerLanguage(uuid string) (string, bool) {
	uuid = strings.TrimPrefix(uuid, checkerScheme+":")
	fields := strings.Split(uuid, "-")
	if len(fields) != 2 {
		return "", false
	}

	return fields[0], true
}

// NewGerritChecker creates a server that periodically checks a gerrit
// server for pending checks.
func NewGerritChecker(server *gerrit.Server, delay time.Duration) (*gerritChecker, error) {
	gc := &gerritChecker{
		server: server,
		todo:   make(chan *gerrit.PendingChecksInfo, 5),
		delay:  delay,
	}

	return gc, nil
}

// errIrrelevant is a marker error value used for checks that don't apply for a change.
var errIrrelevant = errors.New("irrelevant")

// checkChange checks a (change, patchset) for correct formatting in the given language. It returns
// a list of complaints, or the errIrrelevant error if there is nothing to do.
func (c *gerritChecker) checkChange(changeID string, psID int, language string) ([]string, error) {
	ch, err := c.server.GetChange(changeID, strconv.Itoa(psID))
	if err != nil {
		return nil, err
	}
	req := linter.FormatRequest{}
	for n, f := range ch.Files {
		cfg := linter.Formatters[language]
		if cfg == nil {
			return nil, fmt.Errorf("language %q not configured", language)
		}
		if !cfg.Regex.MatchString(n) {
			continue
		}

		req.Files = append(req.Files,
			linter.File{
				Language: language,
				Name:     n,
				Content:  f.Content,
			})
	}
	if len(req.Files) == 0 {
		return nil, errIrrelevant
	}

	rep := linter.FormatReply{}
	if err := linter.Format(&req, &rep); err != nil {
		_, ok := err.(rpc.ServerError)
		if ok {
			return nil, fmt.Errorf("server returned: %s", err)
		}
		return nil, err
	}

	var msgs []string
	for _, f := range rep.Files {
		orig := ch.Files[f.Name]
		if orig == nil {
			return nil, fmt.Errorf("result had unknown file %q", f.Name)
		}
		if !bytes.Equal(f.Content, orig.Content) {
			msg := f.Message
			if msg == "" {
				msg = "found a difference"
			}
			msgs = append(msgs, fmt.Sprintf("%s: %s", f.Name, msg))
			log.Printf("%s/%d: file %s: %s", changeID, psID, f.Name, f.Message)
		} else {
			log.Printf("%s/%d: file %s: OK", changeID, psID, f.Name)
		}
	}

	return msgs, nil
}

func (c *gerritChecker) Serve() {
	for {
		// TODO: real rate limiting.
		wait, err := c.processPendingChecks()
		if err != nil {
			log.Printf("checkAllChecks: %v", err)
		}
		if wait {
			time.Sleep(10 * time.Second)
		}
	}
}

// processPendingChecks
func (c *gerritChecker) processPendingChecks() (wait bool, err error) {
	pending, err := c.server.PendingChecksByScheme(checkerScheme)
	if err != nil {
		wait = true
		return
	}

	if len(pending) == 0 {
		wait = true
		log.Printf("no pending checks")
		return
	}

	// Shuffle so we don't always report the first error if there
	// is a problem.
	rand.Shuffle(len(pending),
		func(i, j int) {
			pending[i], pending[j] = pending[j], pending[i]
		})

	var aggregateErr error
	for _, pc := range pending {
		if err := c.executeCheck(pc); err != nil && aggregateErr == nil {
			// just register the first error.
			aggregateErr = err
		} else if err == nil {
			// we can try again until we stop making progress.
			wait = false
		}
	}
	return wait, aggregateErr
}

// status encodes the checker states.
type status int

var (
	statusUnset      status = 0
	statusIrrelevant status = 4
	statusRunning    status = 1
	statusFail       status = 2
	statusSuccessful status = 3
)

func (s status) String() string {
	return map[status]string{
		statusUnset:      "UNSET",
		statusIrrelevant: "IRRELEVANT",
		statusRunning:    "RUNNING",
		statusFail:       "FAILED",
		statusSuccessful: "SUCCESSFUL",
	}[s]
}

// executeCheck executes the pending checks specified in the argument.
func (gc *gerritChecker) executeCheck(pc *gerrit.PendingChecksInfo) error {
	changeID := strconv.Itoa(pc.PatchSet.ChangeNumber)
	psID := pc.PatchSet.PatchSetID
	for uuid := range pc.PendingChecks {
		now := gerrit.Timestamp(time.Now())
		checkInput := gerrit.CheckInput{
			CheckerUUID: uuid,
			State:       statusRunning.String(),
			Started:     &now,
		}
		log.Printf("change %s, %s set to %q", pc.PatchSet, uuid, statusRunning)
		_, err := gc.server.PostCheck(changeID, psID, &checkInput)
		if err != nil {
			return err
		}

		var status status
		msg := ""
		lang, ok := checkerLanguage(uuid)
		if !ok {
			msg = fmt.Sprintf("uuid %q has unknown language", uuid)
			status = statusFail
		} else {
			msgs, err := gc.checkChange(changeID, psID, lang)
			if err == errIrrelevant {
				status = statusIrrelevant
			} else if err != nil {
				status = statusFail
				log.Printf("checkChange(%s, %d, %q): %v", changeID, psID, lang, err)
				msgs = []string{fmt.Sprintf("tool failure: %v", err)}
			} else if len(msgs) == 0 {
				status = statusSuccessful
			} else {
				status = statusFail
			}
			msg = strings.Join(msgs, ", ")
			if len(msg) > 1000 {
				msg = msg[:995] + "..."
			}
		}

		log.Printf("status %s for %s on %v", status, uuid, pc.PatchSet)
		checkInput = gerrit.CheckInput{
			CheckerUUID: uuid,
			State:       status.String(),
			Message:     msg,
		}

		if _, err := gc.server.PostCheck(changeID, psID, &checkInput); err != nil {
			return err
		}
	}
	return nil
}
