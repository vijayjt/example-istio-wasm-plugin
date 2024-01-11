package main

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/proxytest"
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/types"
)

func TestOnHttpResponseHeaders(t *testing.T) {
	vmTest(t, func(t *testing.T, vm types.VMContext) {
		opt := proxytest.NewEmulatorOption().
			WithPluginConfiguration([]byte(`{"targetURLPrefixes": ["my-host.com"]}`)).
			WithVMContext(vm)
		host, reset := proxytest.NewHostEmulator(opt)
		defer reset()

		require.Equal(t, types.OnPluginStartStatusOK, host.StartPlugin())

		t.Run("http status code is preserved", func(t *testing.T) {
			// Initialize http context.
			id := host.InitializeHttpContext()

			// Call OnHttpRequestHeaders with the headers set to simulate a real request
			hs := [][2]string{{":authority", "my-host.com"}, {":scheme", "https"}, {":path", "/"}}
			action := host.CallOnRequestHeaders(id, hs, false)

			// Call OnHttpResponseHeaders.and set the status code to 503 an error response
			hs = [][2]string{{":status", "503"}, {"key2", "value2"}}
			action = host.CallOnResponseHeaders(id, hs, false)
			require.Equal(t, types.ActionContinue, action)

			// Call OnHttpStreamDone.
			host.CompleteHttpContext(id)

			// Validate the headers are preserved
			resHeaders := host.GetCurrentResponseHeaders(id)
			require.Contains(t, resHeaders, [2]string{":status", "503"})
			require.Contains(t, resHeaders, [2]string{"key2", "value2"})

			// Check Envoy logs.
			logs := host.GetInfoLogs()
			require.Contains(t, logs, "Response eligible for modification to rfc9457 format")
		})
	})
}

func TestOnHttpResponseBody(t *testing.T) {
	type testCase struct {
		statusCode      string
		problemType     string
		expectedAction  types.Action
		errorDetail     string
		path            string
		traceIDHeader   string
		traceID         string
		expectedTraceID string
	}

	vmTest(t, func(t *testing.T, vm types.VMContext) {
		for name, tCase := range map[string]testCase{
			"400": {
				statusCode:      "400",
				problemType:     "https://datatracker.ietf.org/html/rfc9110#section-15.5.1",
				expectedAction:  types.ActionContinue,
				errorDetail:     "something went wrong",
				path:            "/",
				traceIDHeader:   "traceparent",
				traceID:         "",
				expectedTraceID: "00-0aa0000000aa00aa0000aa000a00000a-a0aa0a0000000000-00",
			},
			"401": {
				statusCode:      "401",
				problemType:     "https://datatracker.ietf.org/html/rfc9110#section-15.5.2",
				expectedAction:  types.ActionContinue,
				errorDetail:     "computer says no",
				path:            "/foo",
				traceIDHeader:   "x-request-id",
				traceID:         "10-0aa0000000aa00aa0000aa000a00000a-a0aa0a0000000000-99",
				expectedTraceID: "10-0aa0000000aa00aa0000aa000a00000a-a0aa0a0000000000-99",
			},
			"403": {
				statusCode:      "403",
				problemType:     "https://datatracker.ietf.org/html/rfc9110#section-15.5.4",
				expectedAction:  types.ActionContinue,
				errorDetail:     "fatal error",
				path:            "/foo/bar",
				traceIDHeader:   "x-request-id",
				traceID:         "10-0aa0000000aa00aa0000aa000a00000a-a0aa0a0000000000-89",
				expectedTraceID: "10-0aa0000000aa00aa0000aa000a00000a-a0aa0a0000000000-89",
			},
		} {

			t.Run(name, func(t *testing.T) {
				opt := proxytest.NewEmulatorOption().
					WithPluginConfiguration([]byte(`{"targetURLPrefixes": ["my-host.com"]}`)).
					WithVMContext(vm)
				host, reset := proxytest.NewHostEmulator(opt)
				defer reset()

				require.Equal(t, types.OnPluginStartStatusOK, host.StartPlugin())
				// Initialize http context.
				id := host.InitializeHttpContext()

				// Call OnHttpRequestHeaders with the headers set to simulate a real request
				hs := [][2]string{{":authority", "my-host.com"}, {":scheme", "https"}, {":path", tCase.path}, {tCase.traceIDHeader, tCase.traceID}}
				action := host.CallOnRequestHeaders(id, hs, false)

				// Call OnHttpResponseHeaders setting an error response code
				hs = [][2]string{{":status", tCase.statusCode}}
				action = host.CallOnResponseHeaders(id, hs, false)
				body := []byte(tCase.errorDetail)
				// Set end of stream to True - this should cause the code that actually modifies the response to be executed
				action = host.CallOnResponseBody(id, body, true)

				// Call OnHttpStreamDone.
				host.CompleteHttpContext(id)
				statusCodeInt, _ := strconv.Atoi(tCase.statusCode)

				// Verify the status code is application/problem+json
				resHeaders := host.GetCurrentResponseHeaders(id)
				require.Contains(t, resHeaders, [2]string{":status", tCase.statusCode})
				require.Contains(t, resHeaders, [2]string{"content-type", "application/problem+json"})
				// The type should now be ActionContinue
				require.Equal(t, tCase.expectedAction, action)
				resBody := host.GetCurrentResponseBody(id)
				bodyStr := string(resBody)

				var resp customErrorResponse
				json.Unmarshal([]byte(bodyStr), &resp)
				t.Logf(bodyStr)
				require.Equal(t, tCase.problemType, resp.Type)
				require.Equal(t, "service mesh returned an error", resp.Title)
				require.Equal(t, statusCodeInt, resp.Status)
				require.Equal(t, tCase.expectedTraceID, resp.TraceID)
				require.Equal(t, tCase.path, resp.Instance)
				require.Equal(t, tCase.errorDetail, resp.Detail)

				// Check Envoy logs.
				logs := host.GetInfoLogs()
				require.Contains(t, logs, "Successfully transformed the response to rfc9457 format")
			})
		}

	})
}

// vmTest executes f twice, once with a types.VMContext that executes plugin code directly
// in the host, and again by executing the plugin code within the compiled main.wasm binary.
// Execution with main.wasm will be skipped if the file cannot be found.
func vmTest(t *testing.T, f func(*testing.T, types.VMContext)) {
	t.Helper()

	t.Run("go", func(t *testing.T) {
		f(t, &vmContext{})
	})

	t.Run("wasm", func(t *testing.T) {
		wasm, err := os.ReadFile("custom-errors.wasm")
		if err != nil {
			t.Skip("wasm not found")
		}
		v, err := proxytest.NewWasmVMContext(wasm)
		require.NoError(t, err)
		defer v.Close()
		f(t, v)
	})
}
