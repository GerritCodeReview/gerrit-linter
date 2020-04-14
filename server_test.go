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

package gerritlinter

import (
	"strings"
	"testing"
)

func TestCommitMessage(t *testing.T) {
	for in, want := range map[string]string{
		`abc`: "multiple lines",
		`abc
def
`: "blank line",
		strings.Repeat("x", 80) + "\n": "70 chars",
		`abc.

def`: "end in '.'",
		`abc

def`: "",
	} {
		got := checkCommitMessage(in)

		if want == "" && got != "" {
			t.Errorf("want empty, got %s", got)
		} else if !strings.Contains(got, want) {
			t.Errorf("got %s, want substring %s", got, want)
		}
	}
}

func TestCommitFooter(t *testing.T) {
	for in, want := range map[string]string{
		`abc`: "two paragraphs",
		`abc

def
`: "not found",
		`abc.

myfooter:abc`: "space after",
		`abc

Change-Id: Iabc123
myfooter: value!`: "",
	} {
		got := checkCommitFooter(in, "myfooter")

		if want == "" && got != "" {
			t.Errorf("want empty, got %s", got)
		} else if !strings.Contains(got, want) {
			t.Errorf("got %s, want substring %s", got, want)
		}
	}
}
