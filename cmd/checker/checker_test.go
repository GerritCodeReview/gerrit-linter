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
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/google/gerrit-linter/gerrit"
)

func urlParse(s string) url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}

	return *u
}

type changeInfo struct {
	ChangeId string `json:"change_id"`
	Number   int    `json:"_number"`
}

func TestGerrit(t *testing.T) {
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
	msgChecker, err := gc.PostChecker("gerrit-linter-test", "commitmsg", true)
	if err != nil {
		// create
		msgChecker, err = gc.PostChecker("gerrit-linter-test", "commitmsg", false)
		if err != nil {
			t.Fatalf("create PostChecker: %v", err)
		}
	}
	content, err := g.PostPath("a/changes/",
		"application/json",
		[]byte(`{
  "project": "gerrit-linter-test",
  "subject": "my linter test change.",
  "branch": "master"}
`))
	if err != nil {
		t.Fatal(err)
	}
	var change changeInfo
	if err := gerrit.Unmarshal(content, &change); err != nil {
		t.Fatal(err)
	}
	log.Printf("created change %d", change.Number)

	// This isn't great, but what can we do?
	time.Sleep(100 * time.Millisecond)

	info, err := g.GetCheck(fmt.Sprintf("%d", change.Number), 1, msgChecker.UUID)
	if err != nil {
		t.Fatal(err)
	}

	if want := "FAILED"; info.State != want {
		t.Fatalf("got %q, want %q", info.State, want)
	}

	editURL := g.URL

	editURL.Path = path.Join(editURL.Path, fmt.Sprintf("a/changes/%d/message", change.Number))

	body := fmt.Sprintf(`{"message": "New Commit message\n\nChange-Id: %s\n"}`, change.ChangeId)

	req, err := http.NewRequest("PUT", editURL.String(), bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	rep, err := g.Do(req)
	if err != nil {
		t.Fatalf("PUT message: %v", err)
	}
	defer rep.Body.Close()
	if rep.StatusCode/100 != 2 {
		t.Fatalf("status: %v", rep.Status)
	}

	content, err = ioutil.ReadAll(rep.Body)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	info, err = g.GetCheck(strconv.Itoa(change.Number), 2, msgChecker.UUID)
	if err != nil {
		t.Fatalf("2nd GetCheck: %v", err)
	}

	if info.State != "SUCCESSFUL" {
		t.Fatalf("got %q, want 'SUCCESSFUL'", info.State)
	}

	if _, err := g.PostPath(fmt.Sprintf("a/changes/%d/abandon", change.Number),
		"application/json", []byte(`{"message": "test succeeded"}`)); err != nil {
		t.Fatalf("abandon: %v", err)
	}
}
