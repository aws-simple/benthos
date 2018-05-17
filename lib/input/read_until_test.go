// Copyright (c) 2018 Ashley Jeffs
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package input

import (
	"errors"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/Jeffail/benthos/lib/processor/condition"
	"github.com/Jeffail/benthos/lib/types"
	"github.com/Jeffail/benthos/lib/util/service/log"
	"github.com/Jeffail/benthos/lib/util/service/metrics"
)

func TestReadUntilInput(t *testing.T) {
	content := []byte(`foo
bar
baz`)

	tmpfile, err := ioutil.TempFile("", "benthos_read_until_test")
	if err != nil {
		t.Fatal(err)
	}

	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	inconf := NewConfig()
	inconf.Type = "file"
	inconf.File.Path = tmpfile.Name()
	inconf.File.Multipart = false

	t.Run("ReadUntilBasic", func(te *testing.T) {
		testReadUntilBasic(inconf, te)
	})
	t.Run("ReadUntilRetry", func(te *testing.T) {
		testReadUntilRetry(inconf, te)
	})
	t.Run("ReadUntilEarlyClose", func(te *testing.T) {
		testReadUntilEarlyClose(inconf, te)
	})
	t.Run("ReadUntilInputClose", func(te *testing.T) {
		testReadUntilInputClose(inconf, te)
	})
	t.Run("ReadUntilInputCloseRestart", func(te *testing.T) {
		testReadUntilInputCloseRestart(inconf, te)
	})
}

func testReadUntilBasic(inConf Config, t *testing.T) {
	cond := condition.NewConfig()
	cond.Type = "content"
	cond.Content.Operator = "equals"
	cond.Content.Arg = "bar"

	rConf := NewConfig()
	rConf.Type = "read_until"
	rConf.ReadUntil.Input = &inConf
	rConf.ReadUntil.Condition = cond

	in, err := New(rConf, nil, log.NewLogger(os.Stdout, logConfig), metrics.DudType{})
	if err != nil {
		t.Fatal(err)
	}

	expMsgs := []string{
		"foo",
		"bar",
	}

	for _, exp := range expMsgs {
		var tran types.Transaction
		var open bool
		select {
		case tran, open = <-in.TransactionChan():
			if !open {
				t.Fatal("transaction chan closed")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}

		if act := string(tran.Payload.Get(0)); exp != act {
			t.Errorf("Wrong message contents: %v != %v", act, exp)
		}

		select {
		case tran.ResponseChan <- types.NewSimpleResponse(nil):
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}

	// Should close automatically now
	select {
	case _, open := <-in.TransactionChan():
		if open {
			t.Fatal("transaction chan not closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	if err = in.WaitForClose(time.Second); err != nil {
		t.Fatal(err)
	}
}

func testReadUntilRetry(inConf Config, t *testing.T) {
	cond := condition.NewConfig()
	cond.Type = "content"
	cond.Content.Operator = "equals"
	cond.Content.Arg = "bar"

	rConf := NewConfig()
	rConf.Type = "read_until"
	rConf.ReadUntil.Input = &inConf
	rConf.ReadUntil.Condition = cond

	in, err := New(rConf, nil, log.NewLogger(os.Stdout, logConfig), metrics.DudType{})
	if err != nil {
		t.Fatal(err)
	}

	expMsgs := []string{
		"foo",
		"bar",
	}

	for _, exp := range expMsgs {
		var tran types.Transaction
		var open bool

		// First try
		select {
		case tran, open = <-in.TransactionChan():
			if !open {
				t.Fatal("transaction chan closed")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}

		if act := string(tran.Payload.Get(0)); exp != act {
			t.Errorf("Wrong message contents: %v != %v", act, exp)
		}

		select {
		case tran.ResponseChan <- types.NewSimpleResponse(errors.New("failed")):
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}

		// Second try
		select {
		case tran, open = <-in.TransactionChan():
			if !open {
				t.Fatal("transaction chan closed")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}

		if act := string(tran.Payload.Get(0)); exp != act {
			t.Errorf("Wrong message contents: %v != %v", act, exp)
		}

		select {
		case tran.ResponseChan <- types.NewSimpleResponse(nil):
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}

	// Should close automatically now
	select {
	case _, open := <-in.TransactionChan():
		if open {
			t.Fatal("transaction chan not closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	if err = in.WaitForClose(time.Second); err != nil {
		t.Fatal(err)
	}
}

func testReadUntilEarlyClose(inConf Config, t *testing.T) {
	cond := condition.NewConfig()
	cond.Type = "content"
	cond.Content.Operator = "equals"
	cond.Content.Arg = "bar"

	rConf := NewConfig()
	rConf.Type = "read_until"
	rConf.ReadUntil.Input = &inConf
	rConf.ReadUntil.Condition = cond

	in, err := New(rConf, nil, log.NewLogger(os.Stdout, logConfig), metrics.DudType{})
	if err != nil {
		t.Fatal(err)
	}

	var tran types.Transaction
	var open bool

	select {
	case tran, open = <-in.TransactionChan():
		if !open {
			t.Fatal("transaction chan closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	if act, exp := string(tran.Payload.Get(0)), "foo"; exp != act {
		t.Errorf("Wrong message contents: %v != %v", act, exp)
	}

	select {
	case tran.ResponseChan <- types.NewSimpleResponse(nil):
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	in.CloseAsync()
	if err = in.WaitForClose(time.Second); err != nil {
		t.Fatal(err)
	}
}

func testReadUntilInputClose(inConf Config, t *testing.T) {
	cond := condition.NewConfig()
	cond.Type = "content"
	cond.Content.Operator = "equals"
	cond.Content.Arg = "this never resolves"

	rConf := NewConfig()
	rConf.Type = "read_until"
	rConf.ReadUntil.Input = &inConf
	rConf.ReadUntil.Condition = cond

	in, err := New(rConf, nil, log.NewLogger(os.Stdout, logConfig), metrics.DudType{})
	if err != nil {
		t.Fatal(err)
	}

	expMsgs := []string{
		"foo",
		"bar",
		"baz",
	}

	for _, exp := range expMsgs {
		var tran types.Transaction
		var open bool
		select {
		case tran, open = <-in.TransactionChan():
			if !open {
				t.Fatal("transaction chan closed")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}

		if act := string(tran.Payload.Get(0)); exp != act {
			t.Errorf("Wrong message contents: %v != %v", act, exp)
		}

		select {
		case tran.ResponseChan <- types.NewSimpleResponse(nil):
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}

	// Should close automatically now
	select {
	case _, open := <-in.TransactionChan():
		if open {
			t.Fatal("transaction chan not closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	if err = in.WaitForClose(time.Second); err != nil {
		t.Fatal(err)
	}
}

func testReadUntilInputCloseRestart(inConf Config, t *testing.T) {
	cond := condition.NewConfig()
	cond.Type = "static"
	cond.Static = false

	rConf := NewConfig()
	rConf.Type = "read_until"
	rConf.ReadUntil.Input = &inConf
	rConf.ReadUntil.Condition = cond
	rConf.ReadUntil.Restart = true

	in, err := New(rConf, nil, log.NewLogger(os.Stdout, logConfig), metrics.DudType{})
	if err != nil {
		t.Fatal(err)
	}

	expMsgs := []string{
		"foo",
		"bar",
		"baz",
	}

	// Each loop results in the input being recreated.
	for i := 0; i < 3; i++ {
		for _, exp := range expMsgs {
			var tran types.Transaction
			var open bool
			select {
			case tran, open = <-in.TransactionChan():
				if !open {
					t.Fatal("transaction chan closed")
				}
			case <-time.After(time.Second):
				t.Fatal("timed out")
			}

			if act := string(tran.Payload.Get(0)); exp != act {
				t.Errorf("Wrong message contents: %v != %v", act, exp)
			}

			select {
			case tran.ResponseChan <- types.NewSimpleResponse(nil):
			case <-time.After(time.Second):
				t.Fatal("timed out")
			}
		}
	}

	in.CloseAsync()
	if err = in.WaitForClose(time.Second); err != nil {
		t.Fatal(err)
	}
}
