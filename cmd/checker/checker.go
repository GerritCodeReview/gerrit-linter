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
	"errors"
	"fmt"
	"log"
	"net/rpc"
	"strconv"
	"strings"
	"time"

	"github.com/google/gerritfmt"
	"github.com/google/gerritfmt/gerrit"
)

// gerritChecker run formatting checks against a gerrit server.
type gerritChecker struct {
	server *gerrit.Server

	// UUID => language
	checkerUUIDs []string

	todo chan *gerrit.PendingChecksInfo
}

func checkerLanguage(uuid string) (string, bool) {
	uuid = strings.TrimPrefix(uuid, checkerScheme)
	fields := strings.Split(uuid, "-")
	if len(fields) != 2 {
		return "", false
	}
	return fields[0], true
}

func NewGerritChecker(server *gerrit.Server) (*gerritChecker, error) {
	gc := &gerritChecker{
		server: server,
		todo:   make(chan *gerrit.PendingChecksInfo, 5),
	}

	if out, err := ListCheckers(server); err != nil {
		return nil, err
	} else {
		for _, checker := range out {
			gc.checkerUUIDs = append(gc.checkerUUIDs, checker.UUID)
		}
	}

	go gc.pendingLoop()
	return gc, nil
}

var errIrrelevant = errors.New("irrelevant")

func (c *gerritChecker) checkChange(changeID string, psID int, language string) ([]string, error) {
	ch, err := c.server.GetChange(changeID, strconv.Itoa(psID))
	if err != nil {
		return nil, err
	}
	req := gerritfmt.FormatRequest{}
	for n, f := range ch.Files {
		if !gerritfmt.Formatters[language].Regex.MatchString(n) {
			continue
		}

		req.Files = append(req.Files,
			gerritfmt.File{
				Language: language,
				Name:     n,
				Content:  f.Content,
			})
	}
	if len(req.Files) == 0 {
		return nil, errIrrelevant
	}

	rep := gerritfmt.FormatReply{}
	if err := gerritfmt.Format(&req, &rep); err != nil {
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
			log.Printf("file %s: %s", f.Name, f.Message)
		} else {
			log.Printf("file %s: OK", f.Name)
		}
	}

	return msgs, nil
}

func (c *gerritChecker) pendingLoop() {
	for {
		for _, uuid := range c.checkerUUIDs {
			pending, err := c.server.PendingChecks(uuid)
			if err != nil {
				log.Printf("PendingChecks: %v", err)
				continue
			}
			if len(pending) == 0 {
				log.Printf("no pending checks")
			}

			for _, pc := range pending {
				select {
				case c.todo <- pc:
				default:
					log.Println("too busy; dropping pending check.")
				}
			}
		}
		// TODO: real rate limiting.
		time.Sleep(10 * time.Second)
	}
}

func (gc *gerritChecker) Serve() {
	for p := range gc.todo {
		// TODO: parallelism?.
		if err := gc.executeCheck(p); err != nil {
			log.Printf("executeCheck(%v): %v", p, err)
		}
	}
}

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

func (gc *gerritChecker) executeCheck(pc *gerrit.PendingChecksInfo) error {
	log.Println("checking", pc)

	changeID := strconv.Itoa(pc.PatchSet.ChangeNumber)
	psID := pc.PatchSet.PatchSetID
	for uuid := range pc.PendingChecks {
		now := gerrit.Timestamp(time.Now())
		checkInput := gerrit.CheckInput{
			CheckerUUID: uuid,
			State:       statusRunning.String(),
			Started:     &now,
		}
		log.Printf("posted %s", &checkInput)
		_, err := gc.server.PostCheck(
			changeID, psID, &checkInput)
		if err != nil {
			return err
		}

		var status status
		msg := ""
		lang, ok := checkerLanguage(uuid)
		if !ok {
			return fmt.Errorf("uuid %q had unknown language", uuid)
		} else {
			msgs, err := gc.checkChange(changeID, psID, lang)
			if err == errIrrelevant {
				status = statusIrrelevant
			} else if err != nil {
				status = statusFail
				msgs = []string{fmt.Sprintf("tool failure: %v", err)}
			} else if len(msgs) == 0 {
				status = statusSuccessful
			} else {
				status = statusFail
			}
			msg = strings.Join(msgs, ", ")
			if len(msg) > 80 {
				msg = msg[:77] + "..."
			}
		}

		log.Printf("status %s for lang %s on %v", status, lang, pc.PatchSet)
		checkInput = gerrit.CheckInput{
			CheckerUUID: uuid,
			State:       status.String(),
			Message:     msg,
		}
		log.Printf("posted %s", &checkInput)

		if _, err := gc.server.PostCheck(changeID, psID, &checkInput); err != nil {
			return err
		}
	}
	return nil
}
