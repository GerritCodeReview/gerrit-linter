// Copyright 2020 Google Inc. All rights reserved.
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
	"fmt"
	"log"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/gerrit-linter/gerrit"
)

func TestSchemeLanguage(t *testing.T) {
	lang, ok := checkerLanguage("fmt:commitfooter-Change-Id.1234")
	if !ok {
		t.Fatalf("checkerLanguage failed")
	}
	if want := "commitfooter-Change-Id"; lang != want {
		t.Errorf("got %q, want %q", lang, want)
	}
}

func urlParse(s string) url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}

	return *u
}

type ChangeInfo struct {
	ChangeId string `json:"change_id"`
	Number   int    `json:"_number"`
}

func createUpdateChecker(t *testing.T, gc *gerritChecker, formatter string) *gerrit.CheckerInfo {
	checker, err := gc.PostChecker("gerrit-linter-test", "commitmsg", true)
	if err != nil {
		// create
		checker, err = gc.PostChecker("gerrit-linter-test", "commitmsg", false)
		if err != nil {
			t.Fatalf("create PostChecker: %v", err)
		}
	}
	return checker
}

type ChangeInput struct {
	Project string `json:"project"`
	Subject string `json:"subject"`
	Branch  string `json:"branch"`
}

type EditMessageInput struct {
	Message string `json:"message"`
}

func TestBasic(t *testing.T) {
	g := gerrit.New(urlParse("http://localhost:8080/"))
	g.Authenticator = gerrit.NewBasicAuth("admin:secret")
	g.Debug = true

	gc, err := NewGerritChecker(g, 75*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	go gc.Serve()

	if _, err := g.GetPath("/projects/gerrit-linter-test/"); err != nil {
		t.Fatalf("GetPath: %v", err)
	}

	msgChecker := createUpdateChecker(t, gc, "commitmsg")
	footerLang := "commitfooter-User-Visible"
	footerChecker := createUpdateChecker(t, gc, footerLang)

	changeInput := ChangeInput{
		Project: "gerrit-linter-test",
		Subject: "my linter test change.",
		Branch:  "master",
	}
	var change ChangeInfo
	if err := g.PostPathJSON("a/changes/",
		"application/json",
		&changeInput, &change); err != nil {
		t.Fatal(err)
	}
	log.Printf("created change %d", change.Number)
	defer func() {
		if err := g.PostPathJSON(fmt.Sprintf("a/changes/%d/abandon", change.Number),
			"application/json", &EditMessageInput{Message: "test succeeded"}, &struct{}{}); err != nil {
			log.Printf("abandon: %v", err)
		}
	}()

	gc.processPendingChecks()

	info, err := g.GetCheck(fmt.Sprintf("%d", change.Number), 1, msgChecker.UUID)
	if err != nil {
		t.Fatal(err)
	}

	if info.State != statusFail.String() {
		t.Fatalf("got %q, want %q", info.State, statusFail)
	}

	ignored := ""
	if err := g.PutPathJSON(fmt.Sprintf("a/changes/%d/message", change.Number), "application/json",
		&EditMessageInput{Message: fmt.Sprintf("New Commit message\n\nChange-Id: %s\n", change.ChangeId)},
		&ignored); err != nil {
		t.Fatalf("edit message: %v", err)
	}
	gc.processPendingChecks()

	if info, err = g.GetCheck(strconv.Itoa(change.Number), 2, msgChecker.UUID); err != nil {
		t.Fatalf("2nd GetCheck: %v", err)
	} else if info.State != statusSuccessful.String() {
		t.Fatalf("got %q, want %q", info.State, statusSuccessful)
	}

	if info, err = g.GetCheck(strconv.Itoa(change.Number), 2, footerChecker.UUID); err != nil {
		t.Fatalf("2nd GetCheck: %v", err)
	} else if info.State != statusSuccessful.String() {
		t.Fatalf("got %q, want %q", info.State, statusSuccessful)
	}
}
