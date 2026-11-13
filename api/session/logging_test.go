// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
)

// valueLogger keeps each entry's values as the code passed them, the way a
// writer that escapes nothing would print them.
type valueLogger struct{ entries []map[string]string }

func (l *valueLogger) Debug(_ string, kv ...interface{}) { l.add(kv) }
func (l *valueLogger) Info(_ string, kv ...interface{})  { l.add(kv) }
func (l *valueLogger) Error(_ string, kv ...interface{}) { l.add(kv) }
func (l *valueLogger) With(...interface{}) cmtlog.Logger { return l }

func (l *valueLogger) add(kv []interface{}) {
	entry := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		entry[fmt.Sprint(kv[i])] = fmt.Sprint(kv[i+1])
	}
	l.entries = append(l.entries, entry)
}

// A handshake that fails inside the node is logged with its path and error on
// one line, though the client wrote the path and the error quotes the request.
//
// Rules: [HS-15].
func TestHandshakeFailureStaysOnOneLine(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger := &valueLogger{}
	ctx := context.NewContext().WithLogger(logger)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/accounts/a%0D%0Aforged=1/sessions/7", nil)
	replyError(ctx, c, http.StatusInternalServerError, 5, errors.New("uuid \"a\r\nforged=1\" is not valid"))

	if len(logger.entries) != 1 {
		t.Fatalf("%d entries logged, want 1", len(logger.entries))
	}
	for _, key := range []string{"path", "error"} {
		v, ok := logger.entries[0][key]
		if !ok {
			t.Errorf("no %s logged", key)
		}
		if strings.ContainsAny(v, "\r\n") {
			t.Errorf("%s logged with its line breaks: %q", key, v)
		}
	}
}
