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

package gerrit

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTimestamp(t *testing.T) {
	input := `"2020-04-06 09:06:20.000000000"`
	var ts Timestamp

	if err := json.Unmarshal([]byte(input), &ts); err != nil {
		t.Errorf("Unmarshal: %v", err)
	}

	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Errorf("LoadLocation: %v", err)
	}
	nyTime := Timestamp(time.Time(ts).In(nyc))

	out, err := json.Marshal(&nyTime)
	if err != nil {
		t.Fatalf("json.Marshal(%v): %v", nyTime, err)
	}

	if string(out) != input {
		t.Errorf("got %q, want %q", string(out), input)
	}
}
