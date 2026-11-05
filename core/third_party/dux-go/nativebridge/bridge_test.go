package nativebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("DFILES_NATIVE_TEST_PROCESS") != "1" {
		return
	}
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "--watch" {
		fmt.Println(`{"topic":"volumes"}`)
		os.Exit(0)
	}
	var request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(2)
	}
	switch request.Method {
	case "fixture.echo":
		_ = json.NewEncoder(os.Stdout).Encode(request.Params)
	case "fixture.error":
		fmt.Println(`{"code":"EACCES","error":"denied"}`)
		os.Exit(1)
	case "fixture.invalid":
		fmt.Print("invalid")
	case "fixture.wait", "apps.setDefault", "apps.folderHandler.set":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func fixtureHelper(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("native calls require macOS")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "native-helper")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexec \"$DFILES_NATIVE_TEST_EXE\" -test.run=TestHelperProcess -- \"$@\"\n"), 0o700))
	t.Setenv("DFILES_NATIVE_HELPER", path)
	t.Setenv("DFILES_NATIVE_TEST_EXE", exe)
	t.Setenv("DFILES_NATIVE_TEST_PROCESS", "1")
}

func TestCallRoundTripAndFailures(t *testing.T) {
	fixtureHelper(t)
	require.True(t, Available())
	params := map[string]any{"path": "/tmp/雪 #100%.txt", "value": "'; not shell syntax"}
	var result map[string]any
	require.NoError(t, Call(context.Background(), "fixture.echo", params, &result))
	assert.Equal(t, params, result)
	var coded *Error
	require.ErrorAs(t, Call(context.Background(), "fixture.error", nil, nil), &coded)
	assert.Equal(t, "EACCES", coded.Code)
	assert.Equal(t, "denied", coded.Message)
	require.ErrorContains(t, Call(context.Background(), "fixture.invalid", nil, nil), "invalid JSON")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, Call(ctx, "fixture.wait", nil, nil), context.DeadlineExceeded)
	t.Setenv("DFILES_NATIVE_HELPER", "")
	assert.False(t, Available())
	require.ErrorAs(t, Call(context.Background(), "fixture.echo", nil, nil), &coded)
	assert.Equal(t, "NOTSUPPORTED", coded.Code)
}

func TestInterruptedAssociationChangeHasUnknownOutcome(t *testing.T) {
	fixtureHelper(t)
	for _, method := range []string{"apps.setDefault", "apps.folderHandler.set"} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := Call(ctx, method, nil, nil)
		cancel()
		var coded *Error
		require.ErrorAs(t, err, &coded)
		assert.Equal(t, "ETIMEDOUT", coded.Code)
		assert.Contains(t, coded.Message, "outcome is unknown")
	}
}

func TestSubscribeDecodesAndCloses(t *testing.T) {
	fixtureHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events, err := Subscribe(ctx)
	require.NoError(t, err)
	select {
	case event := <-events:
		assert.Equal(t, "volumes", event.Topic)
	case <-ctx.Done():
		t.Fatal("native event did not arrive")
	}
	select {
	case _, ok := <-events:
		assert.False(t, ok)
	case <-ctx.Done():
		t.Fatal("native event stream did not close")
	}
}

func TestLimitedBufferBoundsRetainedData(t *testing.T) {
	cancelled := false
	buffer := &limitedBuffer{limit: 8, cancel: func() { cancelled = true }}
	n, err := buffer.Write([]byte("12345"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	n, err = buffer.Write([]byte("6789"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.True(t, cancelled)
	assert.True(t, buffer.exceeded)
	assert.Equal(t, "12345678", buffer.String())
	_, err = buffer.Write([]byte(strings.Repeat("x", 1024)))
	require.NoError(t, err)
	assert.Equal(t, 8, buffer.Len())
}
